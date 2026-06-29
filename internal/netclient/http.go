// Package netclient builds outbound network clients from configuration.
package netclient

import (
	"net/http"
	"net/url"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// HTTPClientOptions contains options for HTTP client creation.
type HTTPClientOptions struct {
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
	Logger         *zap.Logger
}

func (opts *HTTPClientOptions) setDefaults() {
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.Logger == nil {
		opts.Logger = zap.L()
	}
}

// HTTPClient returns an HTTP client using proxyURL when configured.
func HTTPClient(name, proxyURL string, opts HTTPClientOptions) (*http.Client, error) {
	opts.setDefaults()

	var (
		transport = http.DefaultTransport
		via       string
	)
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, errors.Wrap(err, "parse proxy url")
		}
		via = u.Host
		transport, ok := transport.(*http.Transport)
		if !ok {
			return nil, errors.Errorf("unexpected transport type: %T", transport)
		}
		transport = transport.Clone()
		transport.Proxy = http.ProxyURL(u)
	}
	transport = otelhttp.NewTransport(transport,
		otelhttp.WithMeterProvider(opts.MeterProvider),
		otelhttp.WithTracerProvider(opts.TracerProvider),
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)
	transport = &loggingRoundTripper{
		name:      name,
		via:       via,
		transport: transport,
		logger:    opts.Logger,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}, nil
}

type loggingRoundTripper struct {
	name      string
	via       string
	transport http.RoundTripper
	logger    *zap.Logger
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	viaField := zap.Skip()
	if l.via != "" {
		viaField = zap.String("via", l.via)
	}
	l.logger.Debug("HTTP request",
		zap.String("client_name", l.name),
		zap.String("method", req.Method),
		zap.String("url", redactURL(req.URL)),
		viaField,
	)
	resp, err := l.transport.RoundTrip(req)
	if err != nil {
		l.logger.Error("HTTP request failed", zap.Error(err))
		return nil, err
	}

	ctField := zap.Skip()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		ctField = zap.String("content_type", ct)
	}
	l.logger.Debug("HTTP response",
		zap.Int("status", resp.StatusCode),
		zap.Int("content_length", int(resp.ContentLength)),
		ctField,
	)
	return resp, nil
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	redacted := *u
	if redacted.User != nil {
		redacted.User = url.UserPassword(redacted.User.Username(), "REDACTED")
	}
	q := redacted.Query()
	for k := range q {
		q.Set(k, "REDACTED")
	}
	redacted.RawQuery = q.Encode()
	return redacted.Redacted()
}
