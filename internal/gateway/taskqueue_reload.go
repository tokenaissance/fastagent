package gateway

import (
	"log/slog"
	"time"
)

// ReloadTaskQueue re-reads the system `taskqueue` namespace and applies it to
// the running queue, so an operator changing maxConcurrent / taskTimeoutSec /
// cronTimeoutSec does not have to roll the pods (docs/session-turn-integrity.md,
// P5). Mirrors ReloadSandbox: same trigger (a system-scope save of that
// namespace), same best-effort contract on the callers' side.
//
// Semantics:
//   - default budget → next task; in-flight tasks keep the budget they started
//     with (executeTask reads it once, under the queue lock);
//   - concurrency limit → next admission; running tasks release into the
//     semaphore they acquired from, so a resize cannot strand a slot;
//   - cron budget → next submission of a cron-sourced turn.
func (g *Gateway) ReloadTaskQueue() error {
	if g.store == nil {
		return nil
	}
	cfg := readSystemTaskQueue(g.store)

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	taskTimeoutSec := cfg.TaskTimeoutSec
	if taskTimeoutSec <= 0 {
		taskTimeoutSec = 300
	}
	var cronTimeout time.Duration
	if cfg.CronTimeoutSec > 0 {
		cronTimeout = time.Duration(cfg.CronTimeoutSec) * time.Second
	}

	if g.taskQueue != nil {
		g.taskQueue.SetMaxConcurrent(maxConcurrent)
		g.taskQueue.SetDefaultTimeout(time.Duration(taskTimeoutSec) * time.Second)
	}
	g.cronTaskTimeoutNs.Store(int64(cronTimeout))

	slog.Info("task queue reloaded",
		"max_concurrent", maxConcurrent,
		"task_timeout", time.Duration(taskTimeoutSec)*time.Second,
		"cron_timeout", cronTimeout)
	return nil
}
