package setup

import "net/http"

// ConfigCommitter applies a persisted configuration change together with the
// side effects that must follow it.
//
// The order is fixed: persist -> invalidate -> optional reloads. Invalidation
// happens before a reload can fail, because the config row is already
// committed at that point; otherwise a failed sandbox/taskqueue reload would
// leave a stale cached UserSpace serving the old values behind a 500.
//
// Callers that own a whole config table (cron, plugins, tools) go through
// saveUserConfig, which uses Commit with no reloads, so their writes cannot
// skip cache invalidation.
type ConfigCommitter struct {
	scope           func(*http.Request) (sc, scopeID string)
	invalidate      func(sc, scopeID string)
	reloadSandbox   func() error
	reloadTaskQueue func() error
}

// CommitOptions selects the runtime reloads a particular patch needs. They are
// derived from the touched namespaces by the caller (the HTTP adapter), not
// guessed here.
type CommitOptions struct {
	ReloadSandbox   bool
	ReloadTaskQueue bool
}

// Commit runs persist (when non-nil), invalidates the request's scope, then
// runs the requested reloads. A persisted change is invalidated even when a
// reload fails.
func (c ConfigCommitter) Commit(r *http.Request, persist func() error, opts CommitOptions) error {
	if persist != nil {
		if err := persist(); err != nil {
			return err
		}
	}

	sc, scopeID := c.scope(r)
	c.invalidate(sc, scopeID)

	if opts.ReloadSandbox && c.reloadSandbox != nil {
		if err := c.reloadSandbox(); err != nil {
			return err
		}
	}
	if opts.ReloadTaskQueue && c.reloadTaskQueue != nil {
		if err := c.reloadTaskQueue(); err != nil {
			return err
		}
	}
	return nil
}
