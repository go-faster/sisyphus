package httpmw

import (
	"context"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

// Wrap applies the standard middleware chain (OTel server instrumentation,
// request logging) used by all of sisyphus's HTTP servers.
func Wrap(lg *zap.Logger, m *app.Telemetry, next http.Handler) http.Handler {
	return otelhttp.NewHandler(Logging(lg)(next), "http.server",
		otelhttp.WithPropagators(m.TextMapPropagator()),
		otelhttp.WithTracerProvider(m.TracerProvider()),
		otelhttp.WithMeterProvider(m.MeterProvider()),
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)
}

// ListenAndServe starts srv.ListenAndServe in the background and logs a
// "<name> listening" line. It returns a buffered channel that receives at
// most one error if the server exits before Shutdown is called.
func ListenAndServe(lg *zap.Logger, name string, srv *http.Server) <-chan error {
	errc := make(chan error, 1)
	go func() {
		lg.Info(name+" listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- errors.Wrap(err, name+" serve")
		}
	}()
	return errc
}

// Shutdown gracefully shuts srv down, bounded by a fixed timeout.
func Shutdown(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// Serve starts srv in the background and blocks until ctx is done or the
// server errors, then shuts it down. It's the common case for HTTP servers
// whose only job is to serve until the process is asked to stop.
func Serve(ctx context.Context, lg *zap.Logger, name string, srv *http.Server) error {
	errc := ListenAndServe(lg, name, srv)
	select {
	case <-ctx.Done():
	case err := <-errc:
		_ = Shutdown(srv)
		return err
	}
	return Shutdown(srv)
}
