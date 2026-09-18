package skills

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// CatalogHandler serves the catalog contract the cloud MCP egress consumes.
//
// It deliberately knows nothing about authentication or which agent is asking:
// the caller mounts it behind whatever the deployment uses for the internal hop
// (in this codebase: a service apikey plus the asserted end-user), so the handler
// stays a pure translation of "these layer directories" into the contract's JSON.
//
// The answer is never cached: the catalog is derived state, and a stale copy would
// be a stale digest set on the other side.
func CatalogHandler(dirs []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		discovered, err := ScanSkillDirs(dirs)
		if err != nil {
			// A partial read must not be presented as the whole catalog: the cloud
			// would serve a digest set that is missing skills, and a host would be
			// told those skills do not exist. Fail loudly instead.
			slog.Warn("skill catalog: incomplete read", "error", err)
			http.Error(w, "catalog could not be read completely: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(BuildCatalog(discovered)); err != nil {
			slog.Warn("skill catalog: encode failed", "error", err)
		}
	})
}
