package agent

// The unified environment-change signal (internal/agent/env_changes.go).
//
// docs/文件系统形式化证明/08 §4: the changes that need a signal are the
// ones the agent did NOT cause — and among those, removals are the hard case,
// because a new thing is often noticed by accident while a removed thing leaves
// no trace. These tests pin the three properties that make the signal trustworthy:
// it states removals, it stays quiet when nothing changed, and it never
// fabricates a change on first observation.

import (
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func snap(skills map[string]string, tools []string, memory string) envSnapshot {
	s := envSnapshot{skills: map[string]string{}, tools: map[string]bool{}}
	for k, v := range skills {
		s.skills[k] = v
	}
	for _, t := range tools {
		s.tools[t] = true
	}
	s.memoryHash = hashMemory(memory)
	return s
}

// snapIdentity returns the same snapshot with identity-file fingerprints and a
// configuration fingerprint attached (docs 10 §3.3, G8/G9).
func snapIdentity(s envSnapshot, identity map[string]string, config string) envSnapshot {
	s.identity = map[string]string{}
	for k, v := range identity {
		s.identity[k] = hashMemory(v)
	}
	s.config = config
	return s
}

// envTurn is the test-side stand-in for "the previous snapshot came from this
// conversation's own receipt": the production code reads it from the turn
// receipt (envBaselineFromReceipt), and the in-process tracker it used to live
// in is gone (docs 10 §4, G20). Tests that do not care where the baseline comes
// from use this; the receipt itself is pinned in config_receipt_test.go.
type envTurn struct {
	prev envSnapshot
	seen bool
}

func newEnvTurn() *envTurn { return &envTurn{} }

// signal renders one turn's delta and makes the current snapshot the next
// turn's previous one — the same two steps a turn performs (render, then stamp).
func (t *envTurn) signal(_ string, cur envSnapshot) string {
	out := renderEnvDelta(t.prev, t.seen, cur)
	t.prev, t.seen = cur, true
	return out
}

// The case the whole mechanism exists for: something the agent relied on is
// GONE. It must be named, with the reminder that absence is not hiding.
func TestEnvSignalCarriesRemovals(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snap(map[string]string{"alpha": "agent|a", "beta": "agent|b"}, []string{"exec", "write_file"}, "remember X"))

	signal := tr.signal("s1", snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, ""))

	if !strings.Contains(signal, "skills removed: beta") {
		t.Fatalf("a removed skill did not appear in the signal:\n%s", signal)
	}
	if !strings.Contains(signal, "tools no longer available: write_file") {
		t.Fatalf("a removed tool did not appear in the signal:\n%s", signal)
	}
	if !strings.Contains(signal, "CLEARED") {
		t.Fatalf("memory being cleared did not appear in the signal:\n%s", signal)
	}
	if !strings.Contains(signal, "gone, not hidden") {
		t.Fatalf("the signal does not warn that absence is not hiding (C2):\n%s", signal)
	}
}

// Additions and in-place edits are stated too, but as their own lines: the
// agent needs to know whether to re-read something it already had.
func TestEnvSignalCarriesAdditionsAndEdits(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snap(map[string]string{"alpha": "agent|old"}, []string{"exec"}, "m1"))

	signal := tr.signal("s1", snap(map[string]string{"alpha": "agent|new", "gamma": "user|g"}, []string{"exec", "browser"}, "m2"))

	for _, want := range []string{"skills added: gamma", "skills changed: alpha", "tools now available: browser", "memory was rewritten"} {
		if !strings.Contains(signal, want) {
			t.Fatalf("signal is missing %q:\n%s", want, signal)
		}
	}
}

// Nothing changed, nothing said (C3): a signal on every turn is a signal the model
// learns to skip.
func TestEnvSignalIsSilentWhenNothingChanged(t *testing.T) {
	tr := newEnvTurn()
	s := snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, "m")
	_ = tr.signal("s1", s)
	if signal := tr.signal("s1", s); signal != "" {
		t.Fatalf("an unchanged environment produced a signal:\n%s", signal)
	}
}

// First observation is not a change. Stating one would invent a fact.
func TestEnvSignalIsSilentOnFirstObservation(t *testing.T) {
	tr := newEnvTurn()
	if signal := tr.signal("fresh", snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, "m")); signal != "" {
		t.Fatalf("the first turn produced a signal:\n%s", signal)
	}
}

// Conversations are tracked independently: another chat's churn is not this
// chat's news (per-chatter memory and per-user skill layers differ per session).
// The isolation is structural now — each conversation's baseline is its OWN turn
// receipt — so what the test pins is that two receipts produce two deltas.
func TestEnvSignalIsPerSession(t *testing.T) {
	base := snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, "m")
	changed := snap(map[string]string{"alpha": "agent|a", "beta": "agent|b"}, []string{"exec"}, "m")

	// s2's receipt is the changed world; s1's is the untouched one.
	if got := renderEnvDelta(changed, true, changed); got != "" {
		t.Fatalf("s2's own receipt should diff clean:\n%s", got)
	}
	if signal := renderEnvDelta(base, true, base); signal != "" {
		t.Fatalf("s1 saw a change that happened in s2:\n%s", signal)
	}
}

// Memory content, not its write time, decides: the background review rewrites
// MEMORY.md regularly, and a rewrite with identical content is not news.
func TestEnvSignalIgnoresContentPreservingMemoryRewrite(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snap(nil, nil, "same facts\n"))
	if signal := tr.signal("s1", snap(nil, nil, "same facts\n")); signal != "" {
		t.Fatalf("an identical memory rewrite produced a signal:\n%s", signal)
	}
}

// A skill list that could not be fully loaded is stated EVEN ON THE FIRST
// TURN. It is not a difference between snapshots — it is a statement that this
// snapshot cannot be trusted, and the failure it prevents is concrete: the
// agent telling the user it lacks a capability it actually has.
func TestEnvSignalCarriesIncompleteSkillListOnFirstTurn(t *testing.T) {
	tr := newEnvTurn()
	s := snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, "m")
	s.skillsIncomplete = true

	signal := tr.signal("fresh", s)

	if !strings.Contains(signal, "could NOT be fully loaded") {
		t.Fatalf("an incomplete skill list was presented as authoritative:\n%s", signal)
	}
	if !strings.Contains(signal, "Do not conclude from this list alone") {
		t.Fatalf("the signal does not say how to behave while the list is incomplete:\n%s", signal)
	}
}

// And it is not repeated once loading recovers, unless something else changed.
func TestEnvSignalStopsCarryingIncompleteSkillsAfterRecovery(t *testing.T) {
	tr := newEnvTurn()
	s := snap(map[string]string{"alpha": "agent|a"}, []string{"exec"}, "m")
	s.skillsIncomplete = true
	if signal := tr.signal("s1", s); signal == "" {
		t.Fatal("expected the incomplete list to be stated")
	}
	s.skillsIncomplete = false
	if signal := tr.signal("s1", s); signal != "" {
		t.Fatalf("a recovered skill list produced a signal:\n%s", signal)
	}
}

// An identity file edited from outside the conversation rewrites part of what
// the agent believes about itself, and the prompt is rebuilt from it every turn:
// the change must be stated, by name and never by content (docs 10 §3.3, G8).
func TestEnvSignalCarriesIdentityFileChanges(t *testing.T) {
	tr := newEnvTurn()
	base := map[string]string{"SOUL.md": "steady", "USER.md": "based in Shanghai"}
	_ = tr.signal("s1", snapIdentity(snap(nil, nil, ""), base, "model=m prompt_mode=agent"))

	// Nothing changed → silence (C3).
	same := tr.signal("s1", snapIdentity(snap(nil, nil, ""), base, "model=m prompt_mode=agent"))
	if same != "" {
		t.Fatalf("an unchanged identity produced a signal:\n%s", same)
	}

	// USER.md is edited from the panel; SOUL.md is untouched.
	edited := map[string]string{"SOUL.md": "steady", "USER.md": "based in Berlin"}
	signal := tr.signal("s1", snapIdentity(snap(nil, nil, ""), edited, "model=m prompt_mode=agent"))
	if !strings.Contains(signal, "identity files changed: USER.md") {
		t.Fatalf("an edited identity file was not named:\n%s", signal)
	}
	if strings.Contains(signal, "SOUL.md") {
		t.Fatalf("an untouched identity file was named:\n%s", signal)
	}
	if strings.Contains(signal, "Berlin") {
		t.Fatalf("the signal leaked file content instead of naming the file:\n%s", signal)
	}

	// A deletion reads differently too, and must be visible.
	signal = tr.signal("s1", snapIdentity(snap(nil, nil, ""), map[string]string{"SOUL.md": "steady"}, "model=m prompt_mode=agent"))
	if !strings.Contains(signal, "identity files changed: USER.md") {
		t.Fatalf("a removed identity file was not named:\n%s", signal)
	}
}

// The agent's own configuration shapes what it can do; a mid-life change is
// stated the same way a changed skill or tool set is (docs 10 §3.3, G9).
func TestEnvSignalCarriesConfigChange(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snapIdentity(snap(nil, nil, ""), nil, "model=small prompt_mode=chatbot"))

	signal := tr.signal("s1", snapIdentity(snap(nil, nil, ""), nil, "model=large prompt_mode=agent"))
	if !strings.Contains(signal, "my configuration changed") {
		t.Fatalf("a changed configuration was not stated:\n%s", signal)
	}
	if !strings.Contains(signal, "model=small") || !strings.Contains(signal, "model=large") {
		t.Fatalf("the signal does not say what changed:\n%s", signal)
	}
}

// The change that used to be invisible for good: a config write REBUILDS the
// agent, so any baseline held in the instance that would have diffed it dies
// with the change it should report. The baseline comes from the conversation's
// own receipt instead, so a brand-new instance states it anyway
// (docs 10 §4, G9 + G20).
func TestEnvSignalStatesAConfigChangeThatOutlivedItsInstance(t *testing.T) {
	// No instance state at all: just the world the previous turn recorded.
	prev := snapIdentity(snap(nil, nil, ""), nil, "model=small prompt_mode=chatbot")
	cur := snapIdentity(snap(nil, nil, ""), nil, "model=large prompt_mode=agent")

	signal := renderEnvDelta(prev, true, cur)
	if !strings.Contains(signal, "my configuration changed") {
		t.Fatalf("a config change that survived an agent rebuild was not stated:\n%s", signal)
	}
	if !strings.Contains(signal, "model=small") || !strings.Contains(signal, "model=large") {
		t.Fatalf("the signal does not say what changed:\n%s", signal)
	}

	// The receipt is a claim about the past: when it matches, there is nothing
	// to say (C3).
	if got := renderEnvDelta(cur, true, cur); got != "" {
		t.Fatalf("a baseline equal to the current configuration produced a signal:\n%s", got)
	}

	// No receipt (first turn, rewritten history, a row written before the stamp
	// existed) must never be phrased as a change.
	if got := renderEnvDelta(envSnapshot{}, false, cur); got != "" {
		t.Fatalf("an unsampled baseline produced a signal:\n%s", got)
	}
}

// snapCron attaches a scheduled-job fingerprint to a snapshot (docs 10 §3.3, G10).
func snapCron(s envSnapshot, jobs map[string]string) envSnapshot {
	s.cron = map[string]string{}
	for id, def := range jobs {
		s.cron[id] = def
	}
	return s
}

// A job deleted from outside — the panel, another session, an operator — turns
// "I will be woken at 9" into a false belief. It must be named (docs 10 §3.3).
func TestEnvSignalCarriesScheduledJobChanges(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snapCron(snap(nil, nil, ""), map[string]string{
		"job1": "morning-brief|0 9 * * *|prompt|enabled=true",
		"job2": "weekly-report|0 9 * * 1|prompt|enabled=true",
	}))

	signal := tr.signal("s1", snapCron(snap(nil, nil, ""), map[string]string{
		"job2": "weekly-report|0 9 * * 5|prompt|enabled=true",
		"job3": "nightly-build|0 2 * * *|prompt|enabled=true",
	}))
	if !strings.Contains(signal, "scheduled jobs no longer exist: morning-brief") {
		t.Fatalf("a deleted job was not named:\n%s", signal)
	}
	if !strings.Contains(signal, "scheduled jobs changed: weekly-report") {
		t.Fatalf("a rescheduled job was not named:\n%s", signal)
	}
	if !strings.Contains(signal, "scheduled jobs added: nightly-build") {
		t.Fatalf("an added job was not named:\n%s", signal)
	}

	// Same jobs, same definitions → silence (C3).
	if again := tr.signal("s1", snapCron(snap(nil, nil, ""), map[string]string{
		"job2": "weekly-report|0 9 * * 5|prompt|enabled=true",
		"job3": "nightly-build|0 2 * * *|prompt|enabled=true",
	})); again != "" {
		t.Fatalf("an unchanged job list produced a signal:\n%s", again)
	}
}

// The fingerprint must ignore the scheduler's run bookkeeping: LastRun/NextRun
// move on every tick, and treating that as a change would fire the signal every
// single turn (C3).
func TestCronFingerprintIgnoresRunBookkeeping(t *testing.T) {
	base := store.CronJobRecord{ID: "job1", Name: "morning-brief", Schedule: "0 9 * * *", Type: "prompt", Enabled: true}
	before := cronFingerprint([]store.CronJobRecord{base})

	ran := base
	now := time.Unix(1_800_000_000, 0)
	ran.LastRun = &now
	ran.NextRun = &now
	ran.FailureCount = 3
	after := cronFingerprint([]store.CronJobRecord{ran})

	if before["job1"] != after["job1"] {
		t.Fatalf("run bookkeeping leaked into the fingerprint:\n before=%q\n after =%q", before["job1"], after["job1"])
	}

	// The definition fields, on the other hand, must show up.
	edited := base
	edited.Schedule = "0 10 * * *"
	if cronFingerprint([]store.CronJobRecord{edited})["job1"] == before["job1"] {
		t.Fatal("a rescheduled job produced the same fingerprint")
	}
}

// An unreadable job list is stated as uncertainty, never as a deletion, and it
// must not be diffed against the previous turn (docs 10 §3.3, G10).
func TestEnvSignalStatesUnreadableJobListWithoutClaimingDeletion(t *testing.T) {
	tr := newEnvTurn()
	_ = tr.signal("s1", snapCron(snap(nil, nil, ""), map[string]string{"job1": "morning-brief|0 9 * * *|prompt|enabled=true"}))

	unreadable := snap(nil, nil, "")
	unreadable.cronIncomplete = true
	signal := tr.signal("s1", unreadable)
	if !strings.Contains(signal, "scheduled-job list could NOT be read") {
		t.Fatalf("an unreadable job list was not stated:\n%s", signal)
	}
	if strings.Contains(signal, "no longer exist") {
		t.Fatalf("an unreadable list was reported as a deletion:\n%s", signal)
	}
}
