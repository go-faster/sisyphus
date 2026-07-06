package mcpserver

import (
	"encoding/json"
	"net/http"
)

// HealthHandler returns a lightweight liveness endpoint for the MCP HTTP server.
func HealthHandler(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Version string `json:"version,omitempty"`
		}{
			Status:  "ok",
			Version: version,
		})
	})
}
