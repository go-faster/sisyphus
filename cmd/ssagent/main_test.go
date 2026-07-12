package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/mcpserver"
)

// fakeInvestigator lets tests control an investigation's outcome and,
// optionally, block until the test signals it to proceed (block) so
// concurrency-limit tests can hold a worker slot deterministically.
type fakeInvestigator struct {
	res   agent.Result
	err   error
	block <-chan struct{}
}

func (f *fakeInvestigator) Investigate(ctx context.Context, description string) (agent.Result, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	return f.res, f.err
}

// fakeJobStore is an in-memory jobStore so handler tests don't need a real
// Postgres connection. notify, if set, receives a job's ID every time
// Complete or Fail resolves it, giving tests a deterministic way to wait for
// the background worker goroutine without sleeping/polling.
type fakeJobStore struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]agentstore.Job
	byKey     map[string]uuid.UUID
	submitErr error
	notify    chan uuid.UUID
}

func newFakeJobStore() *fakeJobStore {
	return &fakeJobStore{jobs: make(map[uuid.UUID]agentstore.Job), byKey: make(map[string]uuid.UUID)}
}

func (s *fakeJobStore) Submit(_ context.Context, key, _ string) (agentstore.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitErr != nil {
		return agentstore.Job{}, false, s.submitErr
	}
	if id, ok := s.byKey[key]; ok {
		return s.jobs[id], false, nil
	}
	id := uuid.New()
	job := agentstore.Job{ID: id, Status: agentstore.StatusPending}
	s.jobs[id] = job
	s.byKey[key] = id
	return job, true, nil
}

func (s *fakeJobStore) MarkRunning(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	job.Status = agentstore.StatusRunning
	s.jobs[id] = job
	return nil
}

func (s *fakeJobStore) Complete(_ context.Context, id uuid.UUID, res agent.Result) error {
	s.mu.Lock()
	job := s.jobs[id]
	job.Status = agentstore.StatusDone
	job.Report = res.Report
	job.Iterations = res.Iterations
	job.ToolsUsed = res.ToolsUsed
	s.jobs[id] = job
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify <- id
	}
	return nil
}

func (s *fakeJobStore) Fail(_ context.Context, id uuid.UUID, cause error) error {
	s.mu.Lock()
	job := s.jobs[id]
	job.Status = agentstore.StatusError
	job.ErrorMessage = cause.Error()
	s.jobs[id] = job
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify <- id
	}
	return nil
}

func (s *fakeJobStore) Get(_ context.Context, id uuid.UUID) (agentstore.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return agentstore.Job{}, agentstore.ErrNotFound
	}
	return job, nil
}

// newTestMux wires the two /investigate routes the same way run() does, so
// tests exercise real method routing ("POST /investigate" vs.
// "GET /investigate/{id}") instead of calling handler funcs directly.
func newTestMux(t *testing.T, store jobStore, inv agent.Investigator, sem chan struct{}) http.Handler {
	t.Helper()
	logger := zaptest.NewLogger(t)
	tracer := noop.NewTracerProvider().Tracer("")
	auth := mcpserver.BearerAuthMiddleware("secret")

	mux := http.NewServeMux()
	mux.Handle("POST /investigate", auth(handleInvestigateSubmit(t.Context(), store, inv, 5*time.Second, 64*1024, sem, tracer, nil, logger)))
	mux.Handle("GET /investigate/{id}", auth(handleInvestigateGet(store, logger)))
	return mux
}

func doRequest(mux http.Handler, method, path, authHeader string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestInvestigate_SubmitAndPoll_HappyPath(t *testing.T) {
	store := newFakeJobStore()
	store.notify = make(chan uuid.UUID, 1)
	inv := &fakeInvestigator{res: agent.Result{
		Report:     agent.Report{Problem: "all good", Verdict: agent.VerdictSolved},
		Iterations: 2,
		ToolsUsed:  1,
	}}
	mux := newTestMux(t, store, inv, nil)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var accepted InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	require.NotEmpty(t, accepted.JobID)
	require.Equal(t, "pending", accepted.Status)

	select {
	case <-store.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to complete")
	}

	rec = doRequest(mux, http.MethodGet, "/investigate/"+accepted.JobID, "Bearer secret", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var job InvestigateJobResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &job))
	require.Equal(t, "done", job.Status)
	require.Equal(t, "all good", job.Problem)
	require.Equal(t, agent.VerdictSolved, job.Verdict)
	require.Equal(t, 2, job.Iterations)
	require.Equal(t, 1, job.ToolsUsed)
}

func TestInvestigate_SubmitRetryWithSameIdempotencyKey_ReturnsSameJob(t *testing.T) {
	store := newFakeJobStore()
	store.notify = make(chan uuid.UUID, 1)
	block := make(chan struct{})
	inv := &fakeInvestigator{block: block}
	mux := newTestMux(t, store, inv, nil)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue", IdempotencyKey: "retry-key"})
	rec1 := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec1.Code)
	var first InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec1.Body.Bytes(), &first))

	rec2 := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusOK, rec2.Code) // not 202: it's a replay, no new job started
	var second InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &second))

	require.Equal(t, first.JobID, second.JobID)

	close(block)
	select {
	case <-store.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to complete")
	}
}

func TestInvestigate_Get_UnknownJob(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store, &fakeInvestigator{}, nil)

	rec := doRequest(mux, http.MethodGet, "/investigate/"+uuid.NewString(), "Bearer secret", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInvestigate_Get_InvalidID(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store, &fakeInvestigator{}, nil)

	rec := doRequest(mux, http.MethodGet, "/investigate/not-a-uuid", "Bearer secret", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_NoAuth(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store, &fakeInvestigator{}, nil)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "", body)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInvestigate_EmptyDescription(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store, &fakeInvestigator{}, nil)

	body, _ := json.Marshal(InvestigateRequest{Description: ""})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_WrongMethod(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store, &fakeInvestigator{}, nil)

	rec := doRequest(mux, http.MethodDelete, "/investigate", "Bearer secret", nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestInvestigate_BodyTooLarge(t *testing.T) {
	store := newFakeJobStore()
	logger := zaptest.NewLogger(t)
	tracer := noop.NewTracerProvider().Tracer("")
	auth := mcpserver.BearerAuthMiddleware("secret")
	mux := http.NewServeMux()
	mux.Handle("POST /investigate", auth(handleInvestigateSubmit(t.Context(), store, &fakeInvestigator{}, 5*time.Second, 16, nil, tracer, nil, logger)))

	body, _ := json.Marshal(InvestigateRequest{Description: "this description is definitely longer than sixteen bytes"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_JobFailure_RecordedAsError(t *testing.T) {
	store := newFakeJobStore()
	store.notify = make(chan uuid.UUID, 1)
	inv := &fakeInvestigator{err: errors.New("boom")}
	mux := newTestMux(t, store, inv, nil)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var accepted InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))

	select {
	case <-store.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to fail")
	}

	rec = doRequest(mux, http.MethodGet, "/investigate/"+accepted.JobID, "Bearer secret", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var job InvestigateJobResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &job))
	require.Equal(t, "error", job.Status)
	require.Equal(t, "boom", job.Error)
}

// TestInvestigate_ConcurrencyLimit_QueuesRatherThanRejects verifies that a
// full worker semaphore no longer produces an HTTP-visible rejection (the
// old synchronous handler's 429): the job is accepted and persisted as
// pending immediately, and only its actual execution waits for a free slot.
func TestInvestigate_ConcurrencyLimit_QueuesRatherThanRejects(t *testing.T) {
	store := newFakeJobStore()
	store.notify = make(chan uuid.UUID, 2)
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // occupy the only slot

	inv := &fakeInvestigator{res: agent.Result{Report: agent.Report{Verdict: agent.VerdictSolved}}}
	mux := newTestMux(t, store, inv, sem)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var accepted InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	id, err := uuid.Parse(accepted.JobID)
	require.NoError(t, err)

	job, err := store.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, agentstore.StatusPending, job.Status)

	// Free the slot; the queued job should now run to completion.
	<-sem
	select {
	case <-store.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued job to run")
	}

	job, err = store.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, agentstore.StatusDone, job.Status)
}
