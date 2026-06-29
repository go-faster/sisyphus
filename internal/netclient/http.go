// Package netclient builds outbound network clients from configuration.
package netclient

import (
	"context"
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
)

// HTTPClientOptions contains options for HTTP client creation.
type HTTPClientOptions struct {
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
	Timeout        time.Duration
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
	transport = otelhttp.NewTransport(transport,
		otelhttp.WithMeterProvider(opts.MeterProvider),
		otelhttp.WithTracerProvider(opts.TracerProvider),
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	)
	transport = &loggingRoundTripper{
		name:      name,
		via:       via,
		transport: transport,
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
	name      string
	via       string
	transport http.RoundTripper
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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
