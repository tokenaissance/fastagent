package tools

import (
	"context"
	"testing"
)

// A sandbox has no process handle to wait on, so the runtime only learns a job
// is over by polling it — which is why retirement hangs off the poll. Without
// it, the table grew by one entry for every job an agent had ever started
// (docs 10 §10, G27).

const sandboxLaunchReply = "fcbg 4242 1\n"

func startAndPoll(t *testing.T, jobs *sandboxJobs, status, body string) *sandboxJob {
	t.Helper()
	r := &fakeSandboxRunner{replies: []string{
		sandboxLaunchReply,
		probeReply(status, len(body), "0", body),
	}}
	j, err := jobs.start(context.Background(), r, "true")
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	if _, err := j.output(context.Background(), nil); err != nil {
		t.Fatalf("poll job: %v", err)
	}
	return j
}

// Falsification: remove the delete loop in retireFinished and the cap assertion
// fails (72 finished jobs stay in the table).
func TestSandboxJobsForgetFinishedJobsBeyondTheRetention(t *testing.T) {
	jobs := newSandboxJobs()
	for i := 0; i < sandboxRetainedFinished+8; i++ {
		startAndPoll(t, jobs, "exited", "done")
	}

	jobs.mu.Lock()
	got := len(jobs.live)
	jobs.mu.Unlock()
	if got > sandboxRetainedFinished {
		t.Fatalf("job table holds %d finished entries, want <= %d", got, sandboxRetainedFinished)
	}
}

// A job still running must never be forgotten: its entry is the only handle
// bash_output has on a live process inside the sandbox.
func TestSandboxJobsNeverForgetARunningJob(t *testing.T) {
	jobs := newSandboxJobs()
	live := startAndPoll(t, jobs, "running", "still going")

	for i := 0; i < sandboxRetainedFinished+8; i++ {
		startAndPoll(t, jobs, "exited", "done")
	}

	if jobs.get(live.id) == nil {
		t.Fatal("a running sandbox job was forgotten")
	}
}
