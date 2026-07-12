package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/agent"
)

func TestClient_Investigate_SubmitThenPollUntilDone(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/investigate":
			require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "test issue", body["description"])
			require.NotEmpty(t, body["idempotency_key"])
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/investigate/job-1":
			if polls.Add(1) < 3 {
				_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"done","problem":"test problem","verdict":"solved"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, Token: "secret", PollInterval: time.Millisecond})
	report, err := c.Investigate(context.Background(), "test issue")
	require.NoError(t, err)
	require.Equal(t, agent.Report{Problem: "test problem", Verdict: agent.VerdictSolved}, report)
	require.GreaterOrEqual(t, polls.Load(), int32(3))
}

func TestClient_Investigate_JobError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"error","error":"boom"}`))
		}
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, PollInterval: time.Millisecond})
	_, err := c.Investigate(context.Background(), "test issue")
	require.ErrorContains(t, err, "boom")
}

func TestClient_Investigate_JobError_MaxIterations(t *testing.T) {
	// The server-side error's type (agent.ErrMaxIterations) doesn't survive
	// the HTTP/JSON boundary as-is — only its rendered text does — so the
	// client must recover it by matching job.Error's text, letting a caller
	// (e.g. internal/bot) distinguish this from a generic failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"error","error":"exceeded max iterations (8)"}`))
		}
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, PollInterval: time.Millisecond})
	_, err := c.Investigate(context.Background(), "test issue")
	require.ErrorIs(t, err, agent.ErrMaxIterations)
}

func TestClient_Investigate_JobError_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"error","error":"investigate: context deadline exceeded"}`))
		}
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, PollInterval: time.Millisecond})
	_, err := c.Investigate(context.Background(), "test issue")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestClient_Investigate_SubmitRetriesOnTransportFailure(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if attempts.Add(1) < 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"done","verdict":"solved"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, PollInterval: time.Millisecond})
	report, err := c.Investigate(context.Background(), "test issue")
	require.NoError(t, err)
	require.Equal(t, agent.VerdictSolved, report.Verdict)
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
}

func TestClient_Investigate_MaxWaitExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`)) // never finishes
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL, PollInterval: time.Millisecond, MaxWait: 20 * time.Millisecond})
	_, err := c.Investigate(context.Background(), "test issue")
	// The deadline can be hit either mid-poll (a request in flight) or
	// between polls (the ticker wait) - both surface as a wrapped
	// context.DeadlineExceeded.
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestClient_CheckHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/readyz", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := New(Options{URL: srv.URL})
	require.NoError(t, c.CheckHealth(context.Background()))
}
