// Package netclient builds outbound network clients from configuration.
package netclient

import (
	"context"
	stderrors "errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"

	"github.com/go-faster/sdk/zctx"
	"github.com/gregjones/httpcache"
)

// HTTPClientOptions contains options for HTTP client creation.
type HTTPClientOptions struct {
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
	Timeout        time.Duration
	Cache          httpcache.Cache
}

func (opts *HTTPClientOptions) setDefaults() {
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
}

// HTTPClient returns an HTTP client using proxyURL when configured.
func HTTPClient(ctx context.Context, name, proxyURL string, opts HTTPClientOptions) (*http.Client, error) {
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
		baseTransport, ok := transport.(*http.Transport)
		if !ok {
			return nil, errors.Errorf("unexpected transport type: %T", transport)
		}
		proxyTransport := baseTransport.Clone()
		if err := configureProxy(proxyTransport, u); err != nil {
			return nil, err
		}
		transport = proxyTransport
	}
	m, err := newClientMetrics(opts.MeterProvider)
	if err != nil {
		return nil, errors.Wrap(err, "create client metrics")
	}
	transport = otelhttp.NewTransport(transport,
		otelhttp.WithMeterProvider(opts.MeterProvider),
		otelhttp.WithTracerProvider(opts.TracerProvider),
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)
	if opts.Cache != nil {
		cacheTransport := httpcache.NewTransport(opts.Cache)
		cacheTransport.Transport = transport
		transport = cacheTransport
	}
	transport = &loggingRoundTripper{
		name:         name,
		via:          via,
		transport:    transport,
		metrics:      m,
		cacheEnabled: opts.Cache != nil,
	}
	_ = ctx
	return &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
	}, nil
}

func configureProxy(transport *http.Transport, u *url.URL) error {
	switch u.Scheme {
	case "", "http", "https":
		transport.Proxy = http.ProxyURL(u)
		return nil
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(u, &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		if err != nil {
			return errors.Wrap(err, "create socks proxy dialer")
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return errors.Errorf("unexpected socks dialer type: %T", dialer)
		}
		transport.Proxy = nil
		transport.DialContext = contextDialer.DialContext
		return nil
	default:
		return errors.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}

type loggingRoundTripper struct {
	name         string
	via          string
	transport    http.RoundTripper
	metrics      *clientMetrics
	cacheEnabled bool
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	logger := zctx.From(req.Context())
	viaField := zap.Skip()
	if l.via != "" {
		viaField = zap.String("via", l.via)
	}
	logger.Debug("HTTP request",
		zap.String("client_name", l.name),
		zap.String("method", req.Method),
		zap.String("url", redactURL(req.URL)),
		viaField,
	)
	resp, err := l.transport.RoundTrip(req)
	if err != nil {
		l.metrics.recordError(req.Context(), l.name, httpErrorType(err), time.Since(start).Seconds())
		logger.Error("HTTP request failed", zap.Error(err))
		return nil, err
	}

	ctField := zap.Skip()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		ctField = zap.String("content_type", ct)
	}
	logger.Debug("HTTP response",
		zap.Int("status", resp.StatusCode),
		zap.Int("content_length", int(resp.ContentLength)),
		zap.Duration("took", time.Since(start)),
		ctField,
	)
	l.metrics.record(req.Context(), l.name, resp.StatusCode, time.Since(start).Seconds())

	if l.cacheEnabled {
		status := "miss"
		if resp.Header.Get("X-From-Cache") == "1" {
			status = "hit"
		} else if req.Method != http.MethodGet && req.Method != http.MethodHead {
			status = "bypass"
		}
		l.metrics.recordCache(req.Context(), l.name, status)
	}

	return resp, nil
}

func httpErrorType(err error) string {
	if stderrors.Is(err, context.Canceled) {
		return "canceled"
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var netErr net.Error
	if stderrors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "transport"
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
