package skills

import (
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// LayerSpec is one filesystem skill layer: where it lives, which object-store
// owner mirrors it, and (for readers) how it ranks.
type LayerSpec struct {
	Layer      string `json:"layer"`
	Dir        string `json:"dir"`
	Owner      string `json:"owner,omitempty"`
	Precedence int    `json:"precedence"` // higher wins; mirrors SkillsLoader's merge order
}

// FilesystemLayers returns every layer that lives on disk, lowest precedence
// first. Callers that only need one scope (an agent, a chatter, the platform
// library) filter the result; callers that scan everything — the reconcile
// command, an audit — iterate it. `extra` (config-driven directories) and
// `bundled` (embedded in the binary) are added by the runtime, not here.
func FilesystemLayers() ([]LayerSpec, error) {
	home, err := config.HomeDir()
	if err != nil {
		return nil, err
	}
	layers := make([]LayerSpec, 0, 8)

	if managed, err := GlobalSkillsDir(); err == nil && managed != "" {
		layers = append(layers, LayerSpec{Layer: "managed", Dir: managed, Owner: GlobalSkillOwner, Precedence: 30})
	}
	for _, uid := range subdirs(filepath.Join(home, "users")) {
		if dir, err := UserSkillsDir(uid); err == nil && dir != "" {
			layers = append(layers, LayerSpec{
				Layer: "personal", Dir: dir, Owner: UserSkillOwner(uid), Precedence: 50,
			})
		}
	}
	for _, agentID := range subdirs(filepath.Join(home, "agents")) {
		if dir, err := AgentSkillsDir(agentID); err == nil && dir != "" {
			layers = append(layers, LayerSpec{Layer: "agent", Dir: dir, Owner: agentID, Precedence: 60})
		}
	}
	return layers, nil
}

// GlobalLayer returns the platform-wide layer on its own.
func GlobalLayer() (LayerSpec, error) {
	dir, err := GlobalSkillsDir()
	if err != nil {
		return LayerSpec{}, err
	}
	return LayerSpec{Layer: "managed", Dir: dir, Owner: GlobalSkillOwner, Precedence: 30}, nil
}

// AgentLayer returns one agent's own layer.
func AgentLayer(agentID string) (LayerSpec, error) {
	dir, err := AgentSkillsDir(agentID)
	if err != nil {
		return LayerSpec{}, err
	}
	return LayerSpec{Layer: "agent", Dir: dir, Owner: agentID, Precedence: 60}, nil
}

// UserLayer returns one chatter's personal layer.
func UserLayer(userID string) (LayerSpec, error) {
	dir, err := UserSkillsDir(userID)
	if err != nil {
		return LayerSpec{}, err
	}
	if dir == "" {
		return LayerSpec{}, os.ErrNotExist
	}
	return LayerSpec{Layer: "personal", Dir: dir, Owner: UserSkillOwner(userID), Precedence: 50}, nil
}

// subdirs lists the directory names under parent, sorted by the caller's use of
// the slice (os.ReadDir already sorts).
func subdirs(parent string) []string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
