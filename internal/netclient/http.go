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
)

// HTTPClientOptions contains options for HTTP client creation.
type HTTPClientOptions struct {
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
}

func (opts *HTTPClientOptions) setDefaults() {
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
}

// HTTPClient returns an HTTP client using proxyURL when configured.
func HTTPClient(proxyURL string, opts HTTPClientOptions) (*http.Client, error) {
	opts.setDefaults()

	transport := http.DefaultTransport
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, errors.Wrap(err, "parse proxy url")
		}
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
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}, nil
}
