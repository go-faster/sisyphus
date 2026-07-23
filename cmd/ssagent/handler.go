package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/index"
)

// jobStore is the subset of *agentstore.Store the HTTP handlers need,
// letting tests substitute an in-memory fake instead of a real Postgres
// connection.
type jobStore interface {
	Submit(ctx context.Context, idempotencyKey, description string) (agentstore.Job, bool, error)
	MarkRunning(ctx context.Context, id uuid.UUID) error
	Complete(ctx context.Context, id uuid.UUID, res agent.Result) error
	Fail(ctx context.Context, id uuid.UUID, cause error) error
	Get(ctx context.Context, id uuid.UUID) (agentstore.Job, error)
}

// InvestigateRequest is the POST /investigate body. IdempotencyKey is
// optional: a caller that wants retry-safety across a dropped connection
// should generate one and reuse it on retry, so the retried submission
// returns the original job instead of starting a duplicate investigation.
// If empty, the server generates one, which means a bare retry (no reused
// key) is NOT deduplicated.
type InvestigateRequest struct {
	Description    string `json:"description"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// InvestigateAcceptedResponse is returned by POST /investigate: the
// investigation runs asynchronously, so the caller polls
// GET /investigate/{id} for the result.
type InvestigateAcceptedResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// InvestigateJobResponse is returned by GET /investigate/{id}.
type InvestigateJobResponse struct {
	JobID      string        `json:"job_id"`
	Status     string        `json:"status"`
	Problem    string        `json:"problem,omitempty"`
	Steps      []string      `json:"steps,omitempty"`
	Verdict    agent.Verdict `json:"verdict,omitempty"`
	Findings   string        `json:"findings,omitempty"`
	Sources    []string      `json:"sources,omitempty"`
	Actions    []string      `json:"actions,omitempty"`
	Iterations int           `json:"iterations,omitempty"`
	ToolsUsed  int           `json:"tools_used,omitempty"`
	Debug      *index.Debug  `json:"debug,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func sendError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: err.Error()})
}

func sendJSON(w http.ResponseWriter, statusCode int, v any, logger *zap.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("encode response", zap.Error(err))
	}
}

// jobResponse converts a persisted Job into the wire response for
// GET /investigate/{id}.
func jobResponse(jobID uuid.UUID, job agentstore.Job) InvestigateJobResponse {
	resp := InvestigateJobResponse{
		JobID:  jobID.String(),
		Status: string(job.Status),
		Error:  job.ErrorMessage,
	}
	if job.Status == agentstore.StatusDone {
		resp.Problem = job.Report.Problem
		resp.Steps = job.Report.Steps
		resp.Verdict = job.Report.Verdict
		resp.Findings = job.Report.Findings
		resp.Sources = job.Report.Sources
		resp.Actions = job.Report.Actions
		resp.Iterations = job.Iterations
		resp.ToolsUsed = job.ToolsUsed
		resp.Debug = job.Report.Debug
		// Report.Debug.DurationMS only covers the LLM loop; fill in the
		// queue wait (submit -> loop start) and true end-to-end time
		// (submit -> done), which is what the user actually experiences.
		if resp.Debug != nil && job.StartedAt != nil && job.CompletedAt != nil {
			resp.Debug.QueuedMS = job.StartedAt.Sub(job.CreatedAt).Milliseconds()
			resp.Debug.TotalMS = job.CompletedAt.Sub(job.CreatedAt).Milliseconds()
		}
	}
	return resp
}

// runJob runs the investigation for a newly created job and persists its
// outcome. ctx is intentionally NOT the request context: the job must keep
// running (and its result must still be recorded) even if the client that
// submitted it disconnects, so callers derive ctx from a long-lived base
// context instead.
func runJob(ctx context.Context, store jobStore, inv agent.Investigator, jobID uuid.UUID, description string, tracer trace.Tracer, metrics *agentMetrics, logger *zap.Logger) {
	start := time.Now()
	status := "ok"
	verdict := ""
	toolsUsed := 0
	reportChars := 0
	defer func() {
		if metrics != nil {
			metrics.record(ctx, status, verdict, time.Since(start).Seconds(), toolsUsed, reportChars)
		}
	}()

	if err := store.MarkRunning(ctx, jobID); err != nil {
		logger.Error("mark job running", zap.Error(err), zap.String("job_id", jobID.String()))
	}

	ctx, span := tracer.Start(ctx, "ssagent.investigate",
		trace.WithAttributes(attribute.Int("description.length", len(description)), attribute.String("job.id", jobID.String())),
	)
	defer span.End()

	res, err := inv.Investigate(ctx, description)
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		// Errors don't carry the OTel trace ID across the ssagent -> ssbot
		// HTTP/JSON boundary (only job.Error's rendered string does, same as
		// classifyJobError below), so it's embedded in the message text here
		// and parsed back out client-side. Lets a failed investigation still
		// point an operator at the right trace.
		if sc := span.SpanContext(); sc.IsValid() {
			err = errors.Wrapf(err, "trace_id=%s", sc.TraceID().String())
		}
		logger.Error("investigation failed", zap.Error(err), zap.String("job_id", jobID.String()))
		if failErr := store.Fail(context.WithoutCancel(ctx), jobID, err); failErr != nil {
			logger.Error("persist job failure", zap.Error(failErr), zap.String("job_id", jobID.String()))
		}
		return
	}

	span.SetAttributes(
		attribute.String("verdict", string(res.Report.Verdict)),
		attribute.Int("iterations", res.Iterations),
		attribute.Int("tools_used", res.ToolsUsed),
		attribute.Int("report.chars", res.Report.CharLen()),
	)
	verdict = string(res.Report.Verdict)
	toolsUsed = res.ToolsUsed
	reportChars = res.Report.CharLen()

	if err := store.Complete(context.WithoutCancel(ctx), jobID, res); err != nil {
		logger.Error("persist job result", zap.Error(err), zap.String("job_id", jobID.String()))
	}
}

// handleInvestigateSubmit serves POST /investigate: it persists a job row,
// queues the investigation, and returns immediately with the job's ID (202).
// A worker in this or any other replica claims it from the queue, so the job
// no longer depends on the process that accepted the request staying alive.
// maxBodyBytes caps the request body.
func handleInvestigateSubmit(store jobStore, maxBodyBytes int64, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if maxBodyBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}

		var req InvestigateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			sendError(w, http.StatusBadRequest, errors.Wrap(err, "decode body"))
			return
		}
		if req.Description == "" {
			sendError(w, http.StatusBadRequest, errors.New("description is required"))
			return
		}
		key := req.IdempotencyKey
		if key == "" {
			key = uuid.NewString()
		}

		job, created, err := store.Submit(r.Context(), key, req.Description)
		if err != nil {
			logger.Error("submit job", zap.Error(err))
			sendError(w, http.StatusInternalServerError, errors.Wrap(err, "submit job"))
			return
		}

		if created {
			sendJSON(w, http.StatusAccepted, InvestigateAcceptedResponse{JobID: job.ID.String(), Status: string(agentstore.StatusPending)}, logger)
			return
		}

		// Idempotent replay: a job with this key already exists (e.g. the
		// client retried after a dropped connection), so return it as-is
		// without starting a second investigation.
		sendJSON(w, http.StatusOK, InvestigateAcceptedResponse{JobID: job.ID.String(), Status: string(job.Status)}, logger)
	}
}

// handleInvestigateGet serves GET /investigate/{id}.
func handleInvestigateGet(store jobStore, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := uuid.Parse(idStr)
		if err != nil {
			sendError(w, http.StatusBadRequest, errors.New("invalid job id"))
			return
		}

		job, err := store.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, agentstore.ErrNotFound) {
				sendError(w, http.StatusNotFound, errors.New("job not found"))
				return
			}
			logger.Error("get job", zap.Error(err), zap.String("job_id", idStr))
			sendError(w, http.StatusInternalServerError, errors.Wrap(err, "get job"))
			return
		}

		sendJSON(w, http.StatusOK, jobResponse(id, job), logger)
	}
}
