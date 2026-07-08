package agentclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/agent"
)

func TestClient_Investigate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/investigate", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"problem":"test problem","verdict":"solved"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, Token: "secret"})
	report, err := c.Investigate(context.Background(), "test issue")
	require.NoError(t, err)
	require.Equal(t, agent.Report{Problem: "test problem", Verdict: agent.VerdictSolved}, report)
}

func TestClient_CheckHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL})
	require.NoError(t, c.CheckHealth(context.Background()))
}
