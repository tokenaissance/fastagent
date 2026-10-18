package tools

import (
	"context"
	"strings"
	"testing"
)

func TestResolveSkillEnvHostPath(t *testing.T) {
	skillDirs := []string{"/home/user/agents/a1/skills"}
	provider := func(skillName string) map[string]string {
		if skillName == "baoyu-skills" {
			return map[string]string{"WECHAT_APP_ID": "wx123", "WECHAT_APP_SECRET": "sec"}
		}
		return nil
	}

	env := resolveSkillEnv(
		"bun /home/user/agents/a1/skills/baoyu-skills/scripts/wechat-api.ts --md article.md",
		provider,
		skillDirs,
	)
	if env == nil || env["WECHAT_APP_ID"] != "wx123" || env["WECHAT_APP_SECRET"] != "sec" {
		t.Fatalf("host-path env injection failed, got: %+v", env)
	}
}

func TestResolveSkillEnvSandboxPath(t *testing.T) {
	provider := func(skillName string) map[string]string {
		if skillName == "baoyu-skills" {
			return map[string]string{"WECHAT_APP_ID": "wx456"}
		}
		return nil
	}

	env := resolveSkillEnv(
		"bun /skills/baoyu-skills/scripts/wechat-api.ts --md article.md",
		provider,
		nil,
	)
	if env == nil || env["WECHAT_APP_ID"] != "wx456" {
		t.Fatalf("sandbox /skills/<name> env injection failed, got: %+v", env)
	}
}

func TestResolveSkillEnvNoMatch(t *testing.T) {
	provider := func(skillName string) map[string]string {
		return map[string]string{"WECHAT_APP_ID": "wx789"}
	}
	skillDirs := []string{"/home/user/agents/a1/skills"}

	if env := resolveSkillEnv("bun /tmp/scripts/wechat-api.ts", provider, skillDirs); env != nil {
		t.Fatalf("command outside any skill dir should get no env, got: %+v", env)
	}
}

func TestSkillEnvForTurnPrefersTurnScope(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetLoadSkillDirs([]string{"/boot/skills"})

	bootProvider := func(string) map[string]string { return map[string]string{"KEY": "boot"} }
	turnProvider := func(string) map[string]string { return map[string]string{"KEY": "turn"} }
	ctx := scopedCtx(&TurnScope{
		SkillDirs: []string{"/turn/skills"},
		SkillEnv:  turnProvider,
	})

	provider, dirs := skillEnvForTurn(ctx, r, bootProvider)
	if got := provider("image-tool")["KEY"]; got != "turn" {
		t.Fatalf("turn provider must win, got %q", got)
	}
	if len(dirs) != 1 || dirs[0] != "/turn/skills" {
		t.Fatalf("turn dirs must win, got %v", dirs)
	}
}

func TestSkillEnvForTurnFallsBackOutsideTurn(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetLoadSkillDirs([]string{"/boot/skills"})

	bootProvider := func(string) map[string]string { return map[string]string{"KEY": "boot"} }
	provider, dirs := skillEnvForTurn(context.Background(), r, bootProvider)
	if got := provider("image-tool")["KEY"]; got != "boot" {
		t.Fatalf("boot provider must answer outside a turn, got %q", got)
	}
	if len(dirs) != 1 || dirs[0] != "/boot/skills" {
		t.Fatalf("fallback dirs must come from LoadSkillDirs, got %v", dirs)
	}
}

func TestSkillEnvForTurnResolvesEachFieldIndependently(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetLoadSkillDirs([]string{"/boot/skills"})

	bootProvider := func(string) map[string]string { return map[string]string{"KEY": "boot"} }
	provider, dirs := skillEnvForTurn(scopedCtx(&TurnScope{
		SkillDirs: []string{"/turn/skills"},
	}), r, bootProvider)
	if got := provider("image-tool")["KEY"]; got != "boot" {
		t.Fatalf("a scope without SkillEnv must keep the fallback provider, got %q", got)
	}
	if len(dirs) != 1 || dirs[0] != "/turn/skills" {
		t.Fatalf("scope dirs must still win, got %v", dirs)
	}

	turnProvider := func(string) map[string]string { return map[string]string{"KEY": "turn"} }
	provider, dirs = skillEnvForTurn(scopedCtx(&TurnScope{
		SkillEnv: turnProvider,
	}), r, bootProvider)
	if got := provider("image-tool")["KEY"]; got != "turn" {
		t.Fatalf("scope provider must win, got %q", got)
	}
	if len(dirs) != 1 || dirs[0] != "/boot/skills" {
		t.Fatalf("a scope without SkillDirs must keep the fallback dirs, got %v", dirs)
	}
}

func TestExecSandboxedUsesTurnSkillEnv(t *testing.T) {
	ex := &bgRecordingExecutor{}
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	bindTestTurn(r, &TurnScope{
		Executor:  ex,
		SkillDirs: []string{"/skills"},
		SkillEnv: func(skill string) map[string]string {
			if skill == "image-tool" {
				return map[string]string{"FAL_KEY": "turn-key"}
			}
			return nil
		},
	})
	RegisterExecWithSkillEnv(r, nil, func(string) map[string]string {
		return map[string]string{"FAL_KEY": "boot-key"}
	})

	if _, err := r.Execute(testTurnCtx(r), "exec",
		`{"command":"python /skills/image-tool/main.py"}`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	commands := ex.commands()
	if len(commands) != 1 {
		t.Fatalf("expected one sandbox command, got %v", commands)
	}
	if !strings.Contains(commands[0], "export FAL_KEY='turn-key'") {
		t.Fatalf("turn skill env did not reach the sandbox command: %q", commands[0])
	}
	if strings.Contains(commands[0], "boot-key") {
		t.Fatalf("boot provider leaked into the turn: %q", commands[0])
	}
}
