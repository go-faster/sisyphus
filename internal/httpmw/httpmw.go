// Package httpmw provides small net/http middlewares shared by ssapi/ssmcp's
// HTTP servers.
package httpmw

import (
	"net/http"
	"time"

	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// Middleware is a net/http middleware.
type Middleware = func(http.Handler) http.Handler

type responseRecorder struct {
	http.ResponseWriter
	status      int
	size        int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(status int) {
	// http.ResponseWriter ignores subsequent calls to WriteHeader.
	// We need to mirror that behavior so we capture the actual status code used.
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	// If Write is called before WriteHeader, Go implicitly sets status to 200.
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}

	n, err := r.ResponseWriter.Write(b)
	r.size += n
	return n, err
}

// Unwrap ensures compatibility with http.ResponseController in Go 1.20+
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// InjectLogger injects logger into request context.
func InjectLogger(lg *zap.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqCtx := r.Context()
			reqCtx = zctx.WithOpenTelemetryZap(reqCtx)
			req := r.WithContext(zctx.Base(reqCtx, lg))
			next.ServeHTTP(w, req)
		})
	}
}

// Logging logs every incoming HTTP request at debug level: method, path,
// status, duration and remote address.
func Logging() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			respContentType := rec.Header().Get("Content-Type")
			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Duration("duration", time.Since(start)),
				zap.Int("response_size", rec.size),
				zap.String("content_type", respContentType),
				zap.String("user_agent", r.UserAgent()),
				zap.String("remote_addr", r.RemoteAddr),
			}
			zctx.From(r.Context()).Debug("got http request", fields...)
		})
	}
}
