package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-faster/errors"

	"github.com/stretchr/testify/require"
)

type stubHealth struct {
	err error
}

func (s stubHealth) CheckHealth(context.Context) error {
	return s.err
}

func TestHealthHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)

	HealthHandler("v1.2.3").ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"status":"ok","version":"v1.2.3"}`, rec.Body.String())
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health", http.NoBody)

	HealthHandler("v1.2.3").ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestHealthHandler_CheckError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)

	ReadinessHandler(stubHealth{err: errors.New("api down")}).ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"status":"unhealthy"}`, rec.Body.String())
}

func TestReadinessHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody)

	ReadinessHandler(stubHealth{}).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"status":"ready"}`, rec.Body.String())
}
