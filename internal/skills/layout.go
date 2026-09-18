package skills

import (
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// Skill layers live in several places and several readers used to compute each
// path on their own: the agent loader, the two setup handlers, the install
// target resolver, the CLI and the skills learner. That is the same class of
// defect the workspace formal record already named — a logical location with no
// single source, so the copies drift. These three resolvers are that source for
// the filesystem layers; anything that needs "where does a layer live" asks
// here instead of rebuilding the join.
//
// Layer -> directory -> object-store owner (mirrors SkillsLoader's merge order,
// lowest precedence first):
//
//	extra    <config ExtraDirs>            ""                        (never mirrored)
//	managed  GlobalSkillsDir()             GlobalSkillOwner          (platform-wide)
//	user     <agent home>/skills           ""                        (pod-local seeds)
//	team     <team dir>/skills             ""                        (shared on disk)
//	personal UserSkillsDir(chatter)        UserSkillOwner(chatter)   (per chatter)
//	agent    AgentSkillsDir(agentID)       agentID                   (owner-curated)
//	bundled  embedded in the binary        ""                        (never mirrored)

// GlobalSkillsDir returns the platform-wide skill directory (<home>/skills).
// It is the directory the Dockerfile populates and the one ShowGlobalSkills
// serves; its object-store owner is GlobalSkillOwner.
func GlobalSkillsDir() (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "skills"), nil
}

// AgentSkillsDir returns an agent's own skill directory
// (<agent home>/skills). Its object-store owner is the agent ID.
func AgentSkillsDir(agentID string) (string, error) {
	homePath, err := config.AgentHomeDir(agentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(homePath, "skills"), nil
}

// UserSkillsDir returns a chatter's personal skill directory
// (<home>/users/<uid>/skills). Empty userID means "no such layer" for legacy /
// single-user installs, so the caller disables it.
func UserSkillsDir(userID string) (string, error) {
	if userID == "" {
		return "", nil
	}
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "users", userID, "skills"), nil
}
