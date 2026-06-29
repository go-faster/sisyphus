package netclient

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func TestHTTPClientDefault(t *testing.T) {
	client, err := HTTPClient("", "", HTTPClientOptions{})
	require.NoError(t, err)
	require.IsType(t, new(otelhttp.Transport), client.Transport)
}

func TestHTTPClientProxy(t *testing.T) {
	client, err := HTTPClient("proxy-test", "http://127.0.0.1:8080", HTTPClientOptions{})
	require.NoError(t, err)
	require.NotSame(t, http.DefaultClient, client)
	require.NotNil(t, client.Transport)
}

func TestHTTPClientInvalidProxy(t *testing.T) {
	_, err := HTTPClient("invalid-proxy", "http://[::1", HTTPClientOptions{})
	require.Error(t, err)
}
