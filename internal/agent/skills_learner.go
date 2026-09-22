package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// SkillsLearner observes complex tasks and extracts reusable skill patterns.
type SkillsLearner struct {
	workspace    string
	provider     provider.Provider
	model        string
	minToolCalls int      // minimum tool calls to consider extracting (default: 3)
	skillDirs    []string // directories to search for the skill-learner skill
	// writer is the `skills/` namespace's single writer (implemented by
	// tools.Registry). Non-nil means a learned SKILL.md lands exactly where a
	// chat-created one does — host disk **plus** the object-store mirror — so
	// sibling pods and the next hydration see it. Nil keeps the legacy
	// write-into-the-workspace behaviour used by local single-user runs and by
	// tests that construct a learner directly.
	writer SkillWriter
}

// SkillWriter is the skills namespace's write side, implemented by
// *tools.Registry. The learner holds this rather than writing files itself:
// two writers for one namespace is how a learned skill ends up on a single
// pod's disk (docs/fs-formal-proof/11-change-register.md row 50, the same
// shape the file tools were fixed for).
type SkillWriter interface {
	// SkillsRoot is the host parent of the `skills/` subtree writes land in
	// (the per-user bucket when wired, else the agent home). "" = not
	// configured, so the caller falls back to its own workspace.
	SkillsRoot() string
	// WriteSkillFile writes skills/<slug>/<rel> under that root and mirrors
	// the skill to the workspace store. Returns the absolute path written.
	WriteSkillFile(ctx context.Context, slug, rel, content string) (string, error)
}

// NewSkillsLearner creates a new SkillsLearner.
func NewSkillsLearner(workspace string, p provider.Provider, model string, skillDirs ...string) *SkillsLearner {
	return &SkillsLearner{
		workspace:    workspace,
		provider:     p,
		model:        model,
		minToolCalls: 3,
		skillDirs:    skillDirs,
	}
}

// SetWriter installs the skills namespace's single writer.
func (sl *SkillsLearner) SetWriter(w SkillWriter) { sl.writer = w }

// SetProvider re-points the learner at the provider the agent currently holds.
// Agent.setProvider calls this, so the extraction call is subject to the same
// piiScrubbing wrapper as every other provider call site (row 49): before this,
// a learner built with a raw provider would have sent unredacted conversation
// text for distillation.
func (sl *SkillsLearner) SetProvider(p provider.Provider) { sl.provider = p }

type extractedSkill struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

type extractionResponse struct {
	Extract bool           `json:"extract"`
	Skill   extractedSkill `json:"skill"`
}

// MaybeExtract checks if the conversation warrants skill extraction.
// Called after agent turns complete. Extracts and saves to workspace/skills/<name>/SKILL.md.
func (sl *SkillsLearner) MaybeExtract(ctx context.Context, messages []provider.Message, toolCallCount int) error {
	if toolCallCount < sl.minToolCalls {
		return nil
	}

	skill, err := sl.extractSkill(ctx, messages)
	if err != nil {
		return fmt.Errorf("extract skill: %w", err)
	}
	if skill == nil {
		return nil
	}
	if !isSinglePathSegment(skill.Slug) {
		// The slug is model-authored and becomes a path segment under
		// `<root>/skills/`. Anything that is not one segment is refused rather
		// than resolved somewhere else in the tree.
		slog.Warn("skill learner: refusing a slug that is not a single path segment", "slug", skill.Slug)
		return nil
	}

	root, viaWriter, err := sl.skillTarget()
	if err != nil {
		return err
	}

	// Check if similar skill already exists
	skillDir := filepath.Join(root, "skills", skill.Slug)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
		slog.Debug("skill already exists, skipping", "slug", skill.Slug)
		return nil
	}

	// Save the extracted skill through the namespace's single writer.
	if _, err := sl.writeSkill(ctx, root, viaWriter, skill.Slug, skill.Content); err != nil {
		return err
	}

	// The learner authors both names: the directory is the slug it asked the
	// model for, the identity is the frontmatter `name` the model wrote. Align
	// them here so a learned skill is publishable over MCP like any other —
	// and when the model wrote a non-conforming (e.g. human-readable) name,
	// FinalizeInstallDir leaves the slug in place and we say so instead of
	// silently shipping two identities.
	if finalName, from, ferr := skills.FinalizeInstallDir(
		filepath.Join(root, "skills"), skill.Slug, skillDir); ferr != nil {
		slog.Warn("skill learner: could not align directory with declared name",
			"slug", skill.Slug, "error", ferr)
	} else if from != "" {
		slog.Info("skill learner: directory aligned with declared name",
			"slug", from, "name", finalName)
	} else if declared, reason := skills.ReadSkillName(skillDir); declared == "" {
		slog.Warn("skill learner: learned skill has no publishable name",
			"slug", skill.Slug, "reason", reason)
	}

	slog.Info("extracted new skill", "name", skill.Name, "slug", skill.Slug)
	return nil
}

// skillTarget resolves where a learned skill lands: the parent of the
// `skills/` subtree, and whether the write must go through the injected single
// writer. The writer wins whenever it has a root configured — that is the
// production shape, and it is what makes the learned skill a sibling-pod
// visible, restart-surviving fact. Without one (local single-user runs, tests
// that build a learner directly) the write falls back to the agent workspace,
// where `<workspace>/skills` is the tree the loader scans anyway.
func (sl *SkillsLearner) skillTarget() (root string, viaWriter bool, err error) {
	if sl.writer != nil {
		if r := sl.writer.SkillsRoot(); r != "" {
			return r, true, nil
		}
	}
	if sl.workspace == "" {
		return "", false, fmt.Errorf("skill learner: no skills root configured")
	}
	return sl.workspace, false, nil
}

func (sl *SkillsLearner) writeSkill(ctx context.Context, root string, viaWriter bool, slug, content string) (string, error) {
	if viaWriter {
		full, err := sl.writer.WriteSkillFile(ctx, slug, "SKILL.md", content)
		if err != nil {
			return "", fmt.Errorf("write skill: %w", err)
		}
		return full, nil
	}
	dir := filepath.Join(root, "skills", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create skill dir: %w", err)
	}
	full := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write skill: %w", err)
	}
	return full, nil
}

// isSinglePathSegment reports whether s can be used as one directory name.
func isSinglePathSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, "/\\\x00")
}

// loadSkillLearnerPrompt loads the skill-learner SKILL.md from disk.
// Falls back to a minimal built-in prompt if not found.
func (sl *SkillsLearner) loadSkillLearnerPrompt() string {
	// Search skill directories for skill-learner SKILL.md
	for _, dir := range sl.skillDirs {
		path := filepath.Join(dir, "fastagent-skill-learner", "SKILL.md")
		if data, err := os.ReadFile(path); err == nil {
			slog.Debug("loaded skill-learner prompt from file", "path", path)
			return string(data)
		}
	}

	// Fallback: minimal built-in prompt
	return fallbackExtractionPrompt
}

const fallbackExtractionPrompt = `Analyze the following conversation and determine if it demonstrates a reusable multi-step skill.

Criteria for extraction:
- The task involved 3+ tool calls in a clear, repeatable sequence
- The task is general enough to be useful in other contexts
- The steps can be described as a clear procedure

If this conversation demonstrates a reusable skill, output JSON:
{"extract": true, "skill": {"name": "Human readable name", "slug": "kebab-case-slug", "description": "One line description", "content": "Full SKILL.md content with YAML frontmatter"}}

If not reusable, output: {"extract": false}

The SKILL.md format uses YAML frontmatter:
---
name: Skill Name
description: What it does
---
Step-by-step instructions in markdown...

Output ONLY the JSON, no markdown fences.`

// extractSkill uses LLM to generate a SKILL.md from the conversation.
func (sl *SkillsLearner) extractSkill(ctx context.Context, messages []provider.Message) (*extractedSkill, error) {
	// Build a summary of the conversation for the extraction prompt
	var sb strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, content))
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("  -> tool: %s(%s)\n", tc.Function.Name, truncate(tc.Function.Arguments, 200)))
		}
	}

	prompt := sl.loadSkillLearnerPrompt()

	extractMsgs := []provider.Message{
		{Role: "system", Content: prompt + "\n\nOutput ONLY the JSON, no markdown fences."},
		{Role: "user", Content: sb.String()},
	}

	resp, err := sl.provider.Chat(ctx, extractMsgs, nil, sl.model, 1024, 0.3)
	if err != nil {
		return nil, err
	}

	var result extractionResponse
	// Try to parse response as JSON
	content := strings.TrimSpace(resp.Content)
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		slog.Debug("skill extraction: LLM response not valid JSON", "error", err)
		return nil, nil
	}

	if !result.Extract || result.Skill.Slug == "" || result.Skill.Content == "" {
		return nil, nil
	}

	return &result.Skill, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
