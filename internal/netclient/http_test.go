package netclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPClientDefault(t *testing.T) {
	client, err := HTTPClient(context.Background(), "", "", HTTPClientOptions{})
	require.NoError(t, err)
	require.IsType(t, &loggingRoundTripper{}, client.Transport)
}

func TestHTTPClientProxy(t *testing.T) {
	client, err := HTTPClient(context.Background(), "proxy-test", "http://127.0.0.1:8080", HTTPClientOptions{})
	require.NoError(t, err)
	require.NotSame(t, http.DefaultClient, client)
	require.NotNil(t, client.Transport)
}

func TestHTTPClientInvalidProxy(t *testing.T) {
	_, err := HTTPClient(context.Background(), "invalid-proxy", "http://[::1", HTTPClientOptions{})
	require.Error(t, err)
}
