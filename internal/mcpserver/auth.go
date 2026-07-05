package mcpserver

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuthMiddleware wraps an http.Handler with a static bearer-token check.
func BearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const prefix = "Bearer "
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), prefix)
			if !ok || token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
