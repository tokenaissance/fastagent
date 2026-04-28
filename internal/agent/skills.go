package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
	"gopkg.in/yaml.v3"
)

// Skill represents a discovered skill.
type Skill struct {
	Name        string            // directory name
	Layer       string            // "agent", "user", "managed", "bundled", "extra"
	Content     string            // contents of SKILL.md (with {baseDir} replaced)
	BaseDir     string            // absolute path to the skill directory
	Description string            // from frontmatter
	Metadata    *SkillMetadata    // parsed OpenClaw metadata
	Gated       bool              // true if gating requirements not met
	GateReason  string            // reason gating failed
}

// SkillFrontmatter represents the YAML frontmatter of a SKILL.md file.
//
// Env is the ergonomic shortcut for declaring configurable environment
// variables — equivalent to writing them under metadata.fastclaw.env
// but spares skill authors the namespace nesting when they don't need
// to publish their skill to a non-fastclaw runtime. The HTTP layer
// merges both sources, top-level Env wins on conflict.
type SkillFrontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Homepage    string         `yaml:"homepage"`
	Env         []SkillEnvSpec `yaml:"env"`
	Metadata    yaml.Node      `yaml:"metadata"`
}

// SkillMetadata represents the skill metadata block.
// Supports both "fastclaw" and "openclaw" keys for backward compatibility.
type SkillMetadata struct {
	FastClaw *OpenClawMeta `json:"fastclaw"`
	OpenClaw *OpenClawMeta `json:"openclaw"`
}

// Meta returns the effective metadata, preferring fastclaw over openclaw.
func (m *SkillMetadata) Meta() *OpenClawMeta {
	if m.FastClaw != nil {
		return m.FastClaw
	}
	return m.OpenClaw
}

// OpenClawMeta holds OpenClaw-specific metadata.
type OpenClawMeta struct {
	Emoji      string           `json:"emoji"`
	Homepage   string           `json:"homepage"`
	Always     bool             `json:"always"`
	OS         []string         `json:"os"`
	Requires   *SkillRequires   `json:"requires"`
	PrimaryEnv string           `json:"primaryEnv"`
	// Env declares configurable environment variables this skill reads.
	// Surfaced to the admin UI so operators get labeled inputs (with
	// help text + secret masking) instead of having to grep main.py for
	// os.environ.get() calls. PrimaryEnv stays around for the legacy
	// "single API key" convenience path; multi-provider skills like
	// image-tool list everything here.
	Env     []SkillEnvSpec  `json:"env,omitempty"`
	Install json.RawMessage `json:"install"`
}

// SkillEnvSpec describes one configurable env var. All fields except
// Name are optional. Secret defaults to true at the UI layer when the
// name matches /KEY|TOKEN|SECRET|PASSWORD/i so authors usually don't
// have to set it.
//
// Carries both json and yaml tags so it round-trips via the
// metadata.fastclaw.env path (yaml→generic→json→struct, json tags) AND
// via the new top-level frontmatter.Env shortcut (yaml→struct directly,
// yaml tags).
type SkillEnvSpec struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// SkillRequires holds gating requirements.
type SkillRequires struct {
	Bins    []string `json:"bins"`
	AnyBins []string `json:"anyBins"`
	Env     []string `json:"env"`
	Config  []string `json:"config"`
}

// SkillsLoader discovers and merges skills from multiple layers with OpenClaw compatibility.
type SkillsLoader struct {
	homeDir   string
	agentDir  string
	teamDir   string
	skillsCfg config.SkillsConfig
	globalCfg config.SkillsCfg
	// workspaceStore is optional: when set, LoadSkills hydrates the global
	// and agent skill directories from the object store before scanning the
	// filesystem. Without this, a skill uploaded to the store after a pod's
	// UserSpace was cached is invisible to that pod until restart — and
	// completely invisible on replicas that didn't handle the upload.
	workspaceStore workspace.Store
	agentID        string
}

// NewSkillsLoader creates a new skills loader.
func NewSkillsLoader(homeDir, agentDir, teamDir string, skillsCfg config.SkillsConfig) *SkillsLoader {
	return &SkillsLoader{
		homeDir:   homeDir,
		agentDir:  agentDir,
		teamDir:   teamDir,
		skillsCfg: skillsCfg,
	}
}

// NewSkillsLoaderWithGlobal creates a skills loader with global SkillsCfg for env injection and entries.
func NewSkillsLoaderWithGlobal(homeDir, agentDir, teamDir string, skillsCfg config.SkillsConfig, globalCfg config.SkillsCfg) *SkillsLoader {
	sl := NewSkillsLoader(homeDir, agentDir, teamDir, skillsCfg)
	sl.globalCfg = globalCfg
	return sl
}

// WithObjectStore wires a workspace.Store + agentID so LoadSkills hydrates
// skills from the object store before scanning the filesystem. Returns the
// loader for chaining.
func (sl *SkillsLoader) WithObjectStore(ws workspace.Store, agentID string) *SkillsLoader {
	sl.workspaceStore = ws
	sl.agentID = agentID
	return sl
}

// LoadSkills discovers skills from all layers and returns them merged.
// Precedence: agent workspace > user installed > managed > extra dirs.
func (sl *SkillsLoader) LoadSkills() []Skill {
	// Mirror object-store skills to the local filesystem so a skill
	// uploaded to OSS (or installed on another replica) is visible here
	// this turn — not on next pod restart. Cheap idempotent hydrate; the
	// store does "skip if size matches" per object.
	if sl.workspaceStore != nil {
		ctx := context.Background()
		managedDir := fastclawManagedDir()
		if managedDir != "" {
			keep := BundledSkillNames()
			if err := skills.HydrateSkillsDown(ctx, sl.workspaceStore, skills.GlobalSkillOwner, managedDir, keep...); err != nil {
				slog.Warn("global skill hydrate failed", "error", err)
			}
		}
		if sl.agentID != "" && sl.agentDir != "" {
			agentSkills := filepath.Join(sl.agentDir, "skills")
			if err := skills.HydrateSkillsDown(ctx, sl.workspaceStore, sl.agentID, agentSkills); err != nil {
				slog.Warn("agent skill hydrate failed", "error", err)
			}
		}
	}

	skillsMap := make(map[string]Skill)

	disabled := make(map[string]bool, len(sl.skillsCfg.Disabled))
	for _, name := range sl.skillsCfg.Disabled {
		disabled[name] = true
	}
	// Also check global entries for enabled: false
	for name, entry := range sl.globalCfg.Entries {
		if !entry.Enabled {
			disabled[name] = true
		}
	}

	// Layer 4 (lowest): extra dirs from config
	for _, dir := range sl.globalCfg.Load.ExtraDirs {
		dir = expandPath(dir)
		for name, skill := range discoverSkillsEnhanced(dir, "extra") {
			if !disabled[name] {
				skillsMap[name] = skill
			}
		}
	}

	// Layer 3: managed skills (~/.fastclaw/skills/)
	managedDir := fastclawManagedDir()
	for name, skill := range discoverSkillsEnhanced(managedDir, "managed") {
		if !disabled[name] {
			skillsMap[name] = skill
		}
	}

	// Layer 2: user installed (~/.fastclaw/skills/)
	userDir := filepath.Join(sl.homeDir, "skills")
	for name, skill := range discoverSkillsEnhanced(userDir, "user") {
		if !disabled[name] {
			skillsMap[name] = skill
		}
	}

	// Layer 1.5: team skills
	if sl.teamDir != "" {
		teamSkillsDir := filepath.Join(sl.teamDir, "skills")
		for name, skill := range discoverSkillsEnhanced(teamSkillsDir, "team") {
			if !disabled[name] {
				skillsMap[name] = skill
			}
		}
	}

	// Layer 1 (highest): agent workspace skills
	agentSkillsDir := filepath.Join(sl.agentDir, "skills")
	for name, skill := range discoverSkillsEnhanced(agentSkillsDir, "agent") {
		if !disabled[name] {
			skillsMap[name] = skill
		}
	}

	// Apply gating and env injection
	result := make([]Skill, 0, len(skillsMap))
	for _, s := range skillsMap {
		if s.Gated {
			slog.Debug("skill gated", "name", s.Name, "reason", s.GateReason)
			continue
		}
		result = append(result, s)
	}
	return result
}

// BuildSkillsSummary returns the skill section of the system prompt.
// All discovered skills are inlined in full — operators install exactly
// the skill set they want for their product agent (no marketplace, no
// lazy load), so the LLM gets the complete SKILL.md content and can
// invoke each skill directly via exec.
func (sl *SkillsLoader) BuildSkillsSummary(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(skillsDirective)
	sb.WriteString("\n\n<skills>\n")
	for _, skill := range skills {
		fmt.Fprintf(&sb, "<skill name=%q layer=%q>\n%s\n</skill>\n", skill.Name, skill.Layer, skill.Content)
	}
	sb.WriteString("</skills>")
	return sb.String()
}

// skillsDirective tells the LLM how to invoke the pre-installed skills.
// Short and assertive — no marketplace mentions, no "load_skill first"
// dance, just "here are your skills, run the script via exec".
const skillsDirective = `<skill_usage_rules>
The skills listed below are this agent's complete toolset. Each skill's full SKILL.md is included inline. To invoke a skill, run its main script via the exec tool and pass arguments on stdin as JSON; the SKILL.md describes args and return shape.
</skill_usage_rules>`

// SkillEnvVars returns environment variables for a specific skill from global config.
func (sl *SkillsLoader) SkillEnvVars(skillName string) map[string]string {
	// Per-agent override wins. Fall back to the global entry only when
	// the agent doesn't have its own row OR has it but it's empty (so
	// the operator doesn't have to copy the global config to every
	// agent just to keep the same defaults).
	var entry config.SkillEntryCfg
	var found bool
	if sl.agentID != "" {
		if agentMap, ok := sl.globalCfg.AgentEntries[sl.agentID]; ok {
			if e, ok := agentMap[skillName]; ok && (e.APIKey != "" || len(e.Env) > 0) {
				entry = e
				found = true
			}
		}
	}
	if !found {
		entry, found = sl.globalCfg.Entries[skillName]
	}
	slog.Info("SkillEnvVars lookup",
		"skillName", skillName,
		"loaderAgentID", sl.agentID,
		"agentEntriesKeys", mapKeys(sl.globalCfg.AgentEntries),
		"entriesKeys", entryKeys(sl.globalCfg.Entries),
		"found", found,
		"entryEnvCount", len(entry.Env))
	if !found {
		return nil
	}
	env := make(map[string]string, len(entry.Env)+1)
	for k, v := range entry.Env {
		env[k] = v
	}
	// If apiKey is set and the skill has a primaryEnv, inject it
	if entry.APIKey != "" {
		// Find the skill to get primaryEnv
		// This is a convenience — the skill's primaryEnv tells us which env var the apiKey maps to
		for _, dir := range sl.allSkillDirs() {
			skillDir := filepath.Join(dir, skillName)
			fm := parseFrontmatter(filepath.Join(skillDir, "SKILL.md"))
			if fm != nil && fm.Metadata.Kind == yaml.MappingNode {
				meta := parseMetadata(&fm.Metadata)
				if meta != nil && meta.Meta() != nil && meta.Meta().PrimaryEnv != "" {
					env[meta.Meta().PrimaryEnv] = entry.APIKey
					break
				}
			}
		}
	}
	return env
}

// AllSkillDirs returns all skill directories in precedence order.
func (sl *SkillsLoader) AllSkillDirs() []string {
	return sl.allSkillDirs()
}

func (sl *SkillsLoader) allSkillDirs() []string {
	var dirs []string
	dirs = append(dirs, filepath.Join(sl.agentDir, "skills"))
	if sl.teamDir != "" {
		dirs = append(dirs, filepath.Join(sl.teamDir, "skills"))
	}
	dirs = append(dirs, filepath.Join(sl.homeDir, "skills"))
	dirs = append(dirs, fastclawManagedDir())
	dirs = append(dirs, sl.globalCfg.Load.ExtraDirs...)
	return dirs
}

// discoverSkillsEnhanced scans a directory for skill subdirectories with SKILL.md,
// parses frontmatter, applies gating, and replaces {baseDir}.
func discoverSkillsEnhanced(dir string, layer string) map[string]Skill {
	result := make(map[string]Skill)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}

		content := strings.TrimSpace(string(data))
		absDir, _ := filepath.Abs(skillDir)

		// Parse frontmatter
		fm := parseFrontmatterFromBytes(data)
		var meta *SkillMetadata
		var desc string
		if fm != nil {
			desc = fm.Description
			if fm.Metadata.Kind == yaml.MappingNode {
				meta = parseMetadata(&fm.Metadata)
			}
		}

		// Replace {baseDir} with the skill's absolute directory path
		content = strings.ReplaceAll(content, "{baseDir}", absDir)

		// Apply gating
		gated, gateReason := checkGating(meta)

		name := entry.Name()
		if fm != nil && fm.Name != "" {
			// Use directory name as the key, but store the frontmatter name
			_ = fm.Name
		}

		result[name] = Skill{
			Name:        name,
			Layer:       layer,
			Content:     content,
			BaseDir:     absDir,
			Description: desc,
			Metadata:    meta,
			Gated:       gated,
			GateReason:  gateReason,
		}
	}

	return result
}

func mapKeys(m map[string]map[string]config.SkillEntryCfg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func entryKeys(m map[string]config.SkillEntryCfg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// SplitSkillFrontmatter is the exported entrypoint used by the HTTP
// layer when it needs both the parsed frontmatter and the raw body
// (e.g. to fall back to the first body line for the description on
// frontmatter-less skills). Returns nil + raw input when there is no
// `---` frontmatter to parse.
func SplitSkillFrontmatter(data []byte) (*SkillFrontmatter, string) {
	fm := parseFrontmatterFromBytes(data)
	body := string(data)
	if fm == nil {
		return nil, body
	}
	// Strip the frontmatter block from the body so callers don't see the
	// YAML lines when scanning for the first prose line.
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "---") {
		rest := trimmed[3:]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			after := rest[end+len("\n---"):]
			body = strings.TrimLeft(after, "\n")
		}
	}
	return fm, body
}

// ParseSkillMetadata is the exported wrapper around the (yaml.Node →
// SkillMetadata) decode path. The HTTP skill list handler uses it to
// surface envSpec to the admin UI.
func ParseSkillMetadata(node *yaml.Node) *SkillMetadata {
	return parseMetadata(node)
}

// parseFrontmatter reads and parses YAML frontmatter from a SKILL.md file path.
func parseFrontmatter(path string) *SkillFrontmatter {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseFrontmatterFromBytes(data)
}

// parseFrontmatterFromBytes parses YAML frontmatter from raw bytes.
func parseFrontmatterFromBytes(data []byte) *SkillFrontmatter {
	content := string(data)

	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return nil
	}

	// Find opening and closing ---
	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:] // skip first ---
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return nil
	}

	fmStr := rest[:endIdx]

	var fm SkillFrontmatter
	if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
		return nil
	}
	return &fm
}

// parseMetadata converts the yaml.Node metadata into our SkillMetadata struct.
func parseMetadata(node *yaml.Node) *SkillMetadata {
	if node == nil || node.Kind == 0 {
		return nil
	}
	// Marshal back to YAML then decode as JSON-like structure
	yamlBytes, err := yaml.Marshal(node)
	if err != nil {
		return nil
	}

	// Unmarshal YAML into a generic map, then marshal to JSON, then unmarshal to struct
	var raw interface{}
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return nil
	}

	jsonBytes, err := json.Marshal(convertYAMLToJSON(raw))
	if err != nil {
		return nil
	}

	var meta SkillMetadata
	if err := json.Unmarshal(jsonBytes, &meta); err != nil {
		return nil
	}
	return &meta
}

// convertYAMLToJSON converts YAML map[string]interface{} (which uses map[interface{}]interface{})
// to JSON-compatible map[string]interface{}.
func convertYAMLToJSON(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			result[k] = convertYAMLToJSON(v)
		}
		return result
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			result[fmt.Sprint(k)] = convertYAMLToJSON(v)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v := range val {
			result[i] = convertYAMLToJSON(v)
		}
		return result
	default:
		return v
	}
}

// checkGating validates whether a skill's requirements are met.
// Returns (gated, reason). gated=true means the skill should be skipped.
func checkGating(meta *SkillMetadata) (bool, string) {
	if meta == nil || meta.Meta() == nil {
		return false, ""
	}
	oc := meta.Meta()

	if oc.Always {
		return false, ""
	}

	// Check OS requirement
	if len(oc.OS) > 0 {
		currentOS := runtime.GOOS
		found := false
		for _, os := range oc.OS {
			if os == currentOS {
				found = true
				break
			}
		}
		if !found {
			return true, fmt.Sprintf("requires OS %v, current is %s", oc.OS, currentOS)
		}
	}

	if oc.Requires == nil {
		return false, ""
	}

	// Check required binaries
	for _, bin := range oc.Requires.Bins {
		if _, err := exec.LookPath(bin); err != nil {
			return true, fmt.Sprintf("required binary %q not found on PATH", bin)
		}
	}

	// Check anyBins (at least one must exist)
	if len(oc.Requires.AnyBins) > 0 {
		found := false
		for _, bin := range oc.Requires.AnyBins {
			if _, err := exec.LookPath(bin); err == nil {
				found = true
				break
			}
		}
		if !found {
			return true, fmt.Sprintf("none of required binaries %v found on PATH", oc.Requires.AnyBins)
		}
	}

	// Check required env vars
	for _, envVar := range oc.Requires.Env {
		if os.Getenv(envVar) == "" {
			return true, fmt.Sprintf("required env var %q not set", envVar)
		}
	}

	return false, ""
}

// fastclawManagedDir returns the FastClaw managed skills directory.
// Honors FASTCLAW_HOME so multi-instance dev (one stack per product)
// doesn't all share /Users/<u>/.fastclaw/skills/.
func fastclawManagedDir() string {
	if h := os.Getenv("FASTCLAW_HOME"); h != "" {
		return filepath.Join(h, "skills")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".fastclaw", "skills")
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// FindSkillForPath returns the skill name if the given path is within a skill directory.
func FindSkillForPath(path string, skillDirs []string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	for _, dir := range skillDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
			// Extract skill name (first component after the skills dir)
			rel, err := filepath.Rel(absDir, absPath)
			if err != nil {
				continue
			}
			parts := strings.SplitN(rel, string(filepath.Separator), 2)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return ""
}
