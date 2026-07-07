package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// HealthChecker verifies dependencies required to serve traffic.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

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

// ReadinessHandler returns a readiness endpoint that verifies external dependencies.
func ReadinessHandler(checks ...HealthChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		for _, check := range checks {
			if check == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			err := check.CheckHealth(ctx)
			cancel()
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(struct {
					Status string `json:"status"`
				}{Status: "unhealthy"})
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
		}{
			Status: "ready",
		})
	})
}
