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
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/mcpserver"
	"github.com/go-faster/sisyphus/internal/queue"
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
	job := agentstore.Job{ID: id, Status: agentstore.StatusPending, CreatedAt: time.Now()}
	s.jobs[id] = job
	s.byKey[key] = id
	return job, true, nil
}

func (s *fakeJobStore) MarkRunning(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	job.Status = agentstore.StatusRunning
	now := time.Now()
	job.StartedAt = &now
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
	now := time.Now()
	job.CompletedAt = &now
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
	now := time.Now()
	job.CompletedAt = &now
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
func newTestMux(t *testing.T, store jobStore) http.Handler {
	t.Helper()
	logger := zaptest.NewLogger(t)
	auth := mcpserver.BearerAuthMiddleware("secret")

	mux := http.NewServeMux()
	mux.Handle("POST /investigate", auth(handleInvestigateSubmit(store, 64*1024, logger)))
	mux.Handle("GET /investigate/{id}", auth(handleInvestigateGet(store, logger)))
	return mux
}

// runClaimed drives the worker handler for one job, standing in for the
// queue worker that would claim it in a running ssagent.
func runClaimed(t *testing.T, store jobStore, inv agent.Investigator, id uuid.UUID, description string) error {
	t.Helper()
	payload, err := json.Marshal(agentstore.Payload{Description: description})
	require.NoError(t, err)
	h := investigateHandler(store, inv, noop.NewTracerProvider().Tracer(""), nil, zaptest.NewLogger(t))
	return h(t.Context(), queue.Delivery{ID: id, Key: id.String(), Payload: payload, Attempts: 1, MaxAttempts: 2})
}

// jobID parses the job ID out of a 202 submit response.
func jobID(t *testing.T, rec *httptest.ResponseRecorder) (InvestigateAcceptedResponse, uuid.UUID) {
	t.Helper()
	var accepted InvestigateAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
	id, err := uuid.Parse(accepted.JobID)
	require.NoError(t, err)
	return accepted, id
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
	mux := newTestMux(t, store)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)

	accepted, id := jobID(t, rec)
	require.NotEmpty(t, accepted.JobID)
	require.Equal(t, "pending", accepted.Status)

	require.NoError(t, runClaimed(t, store, inv, id, "test issue"))

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

// TestJobResponse_QueuedAndTotalDuration verifies that jobResponse fills in
// QueuedMS/TotalMS from the job's CreatedAt/StartedAt/CompletedAt timestamps,
// since Report.Debug.DurationMS alone only covers the LLM loop and misses
// time spent waiting for a free worker slot (the reported bug: a Telegram
// user seeing a much shorter "duration" than the actual wait).
func TestJobResponse_QueuedAndTotalDuration(t *testing.T) {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	started := base.Add(90 * time.Second) // queued for 90s behind the concurrency limit
	completed := started.Add(23 * time.Second)

	cases := []struct {
		name       string
		job        agentstore.Job
		wantQueued int64
		wantTotal  int64
		wantNilDbg bool
	}{
		{
			name: "done with timestamps",
			job: agentstore.Job{
				Status:      agentstore.StatusDone,
				Report:      agent.Report{Debug: &index.Debug{DurationMS: 23000}},
				CreatedAt:   base,
				StartedAt:   &started,
				CompletedAt: &completed,
			},
			wantQueued: 90_000,
			wantTotal:  113_000,
		},
		{
			name: "done but missing timestamps leaves debug untouched",
			job: agentstore.Job{
				Status: agentstore.StatusDone,
				Report: agent.Report{Debug: &index.Debug{DurationMS: 23000}},
			},
			wantQueued: 0,
			wantTotal:  0,
		},
		{
			name: "no debug info configured",
			job: agentstore.Job{
				Status:      agentstore.StatusDone,
				Report:      agent.Report{},
				CreatedAt:   base,
				StartedAt:   &started,
				CompletedAt: &completed,
			},
			wantNilDbg: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := jobResponse(uuid.New(), tc.job)
			if tc.wantNilDbg {
				require.Nil(t, resp.Debug)
				return
			}
			require.NotNil(t, resp.Debug)
			require.Equal(t, tc.wantQueued, resp.Debug.QueuedMS)
			require.Equal(t, tc.wantTotal, resp.Debug.TotalMS)
			require.Equal(t, int64(23000), resp.Debug.DurationMS)
		})
	}
}

func TestInvestigate_SubmitRetryWithSameIdempotencyKey_ReturnsSameJob(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

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
}

func TestInvestigate_Get_UnknownJob(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

	rec := doRequest(mux, http.MethodGet, "/investigate/"+uuid.NewString(), "Bearer secret", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInvestigate_Get_InvalidID(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

	rec := doRequest(mux, http.MethodGet, "/investigate/not-a-uuid", "Bearer secret", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_NoAuth(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "", body)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInvestigate_EmptyDescription(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

	body, _ := json.Marshal(InvestigateRequest{Description: ""})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_WrongMethod(t *testing.T) {
	store := newFakeJobStore()
	mux := newTestMux(t, store)

	rec := doRequest(mux, http.MethodDelete, "/investigate", "Bearer secret", nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestInvestigate_BodyTooLarge(t *testing.T) {
	store := newFakeJobStore()
	logger := zaptest.NewLogger(t)
	auth := mcpserver.BearerAuthMiddleware("secret")
	mux := http.NewServeMux()
	mux.Handle("POST /investigate", auth(handleInvestigateSubmit(store, 16, logger)))

	body, _ := json.Marshal(InvestigateRequest{Description: "this description is definitely longer than sixteen bytes"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInvestigate_JobFailure_RecordedAsError(t *testing.T) {
	store := newFakeJobStore()
	store.notify = make(chan uuid.UUID, 1)
	inv := &fakeInvestigator{err: errors.New("boom")}
	mux := newTestMux(t, store)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)
	accepted, id := jobID(t, rec)

	// A failed investigation is acknowledged, not retried: the failure is
	// already recorded, and another LLM run would fail the same way.
	require.NoError(t, runClaimed(t, store, inv, id, "test issue"))

	rec = doRequest(mux, http.MethodGet, "/investigate/"+accepted.JobID, "Bearer secret", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var job InvestigateJobResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &job))
	require.Equal(t, "error", job.Status)
	require.Equal(t, "boom", job.Error)
}

// TestInvestigate_SubmitIsPendingUntilAWorkerClaimsIt verifies that
// submission only queues work: nothing runs until a worker claims the job, so
// a burst of requests is accepted and persisted rather than rejected or run
// inline. Concurrency limiting now lives in the queue worker.
func TestInvestigate_SubmitIsPendingUntilAWorkerClaimsIt(t *testing.T) {
	store := newFakeJobStore()
	inv := &fakeInvestigator{res: agent.Result{Report: agent.Report{Verdict: agent.VerdictSolved}}}
	mux := newTestMux(t, store)

	body, _ := json.Marshal(InvestigateRequest{Description: "test issue"})
	rec := doRequest(mux, http.MethodPost, "/investigate", "Bearer secret", body)
	require.Equal(t, http.StatusAccepted, rec.Code)
	_, id := jobID(t, rec)

	job, err := store.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, agentstore.StatusPending, job.Status, "submission must not run the investigation")

	require.NoError(t, runClaimed(t, store, inv, id, "test issue"))

	job, err = store.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, agentstore.StatusDone, job.Status)
}

// TestInvestigate_UndecodablePayloadIsRetired verifies a payload that can
// never decode is failed and acknowledged rather than reclaimed until its
// attempts run out.
func TestInvestigate_UndecodablePayloadIsRetired(t *testing.T) {
	store := newFakeJobStore()
	id := uuid.New()
	_, _, err := store.Submit(t.Context(), "key", "test issue")
	require.NoError(t, err)

	h := investigateHandler(store, &fakeInvestigator{}, noop.NewTracerProvider().Tracer(""), nil, zaptest.NewLogger(t))
	require.NoError(t, h(t.Context(), queue.Delivery{ID: id, Payload: []byte("not json"), Attempts: 1, MaxAttempts: 2}))
}
