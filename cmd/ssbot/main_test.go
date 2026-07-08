package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/mcpserver"
)

func TestNewHealthMux(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	mcpserver.InstallHealth(mux, "0.1.0")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var got struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, "ok", got.Status)
	require.Equal(t, "0.1.0", got.Version)
}
