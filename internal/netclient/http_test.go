package netclient

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPClientDefault(t *testing.T) {
	client, err := HTTPClient("")
	require.NoError(t, err)
	require.Same(t, http.DefaultClient, client)
}

func TestHTTPClientProxy(t *testing.T) {
	client, err := HTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.NotSame(t, http.DefaultClient, client)
	require.NotNil(t, client.Transport)
}

func TestHTTPClientInvalidProxy(t *testing.T) {
	_, err := HTTPClient("http://[::1")
	require.Error(t, err)
}
