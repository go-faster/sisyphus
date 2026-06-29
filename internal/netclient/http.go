// Package netclient builds outbound network clients from configuration.
package netclient

import (
	"net/http"
	"net/url"
	"time"

	"github.com/go-faster/errors"
)

// HTTPClient returns an HTTP client using proxyURL when configured.
func HTTPClient(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return http.DefaultClient, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, errors.Wrap(err, "parse proxy url")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.Errorf("unexpected transport type: %T", http.DefaultTransport)
	}
	transport = transport.Clone()
	transport.Proxy = http.ProxyURL(u)
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}, nil
}
