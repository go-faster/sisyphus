// Package agentstore persists ssagent's /investigate jobs in Postgres, so an
// investigation survives a dropped client connection or an ssagent restart:
// the HTTP handler only creates a job row and returns its ID, a worker
// updates the row as the investigation runs, and the client polls for the
// result instead of holding one long-lived request open.
package agentstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/investigationjob"
)

// Status is an InvestigationJob's lifecycle state.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusError   Status = "error"
)

// ErrNotFound is returned by Get when no job matches the given ID.
var ErrNotFound = errors.New("job not found")

// Job is the persisted state of one /investigate request. CreatedAt/StartedAt/
// CompletedAt let callers report true end-to-end timing (submit -> done),
// as opposed to agent.Report.Debug.DurationMS, which only covers the LLM
// loop itself and misses time spent waiting for a free worker slot.
type Job struct {
	ID           uuid.UUID
	Status       Status
	Report       agent.Report
	Iterations   int
	ToolsUsed    int
	ErrorMessage string
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// Store persists InvestigationJob rows via ent.
type Store struct {
	db *ent.Client
}

// New creates a Store backed by db.
func New(db *ent.Client) *Store {
	return &Store{db: db}
}

// Submit creates a new job for idempotencyKey, or returns the already
// existing job if idempotencyKey was submitted before. created reports
// whether this call created the job: callers must only dispatch a worker
// to run the investigation when created is true, so a retried submission
// (same idempotency key) never starts a second, duplicate run.
func (s *Store) Submit(ctx context.Context, idempotencyKey, description string) (job Job, created bool, err error) {
	m, err := s.db.InvestigationJob.Create().
		SetIdempotencyKey(idempotencyKey).
		SetDescription(description).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			existing, getErr := s.db.InvestigationJob.Query().
				Where(investigationjob.IdempotencyKey(idempotencyKey)).
				Only(ctx)
			if getErr != nil {
				return Job{}, false, errors.Wrap(getErr, "get existing job")
			}
			job, err = toJob(existing)
			return job, false, err
		}
		return Job{}, false, errors.Wrap(err, "create job")
	}
	job, err = toJob(m)
	return job, true, err
}

// MarkRunning transitions a job to StatusRunning.
func (s *Store) MarkRunning(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	err := s.db.InvestigationJob.UpdateOneID(id).
		SetStatus(string(StatusRunning)).
		SetStartedAt(now).
		Exec(ctx)
	return errors.Wrap(err, "mark job running")
}

// Complete records a successful investigation result and transitions the job
// to StatusDone.
func (s *Store) Complete(ctx context.Context, id uuid.UUID, res agent.Result) error {
	data, err := reportToMap(res.Report)
	if err != nil {
		return errors.Wrap(err, "encode report")
	}
	err = s.db.InvestigationJob.UpdateOneID(id).
		SetStatus(string(StatusDone)).
		SetReport(data).
		SetIterations(res.Iterations).
		SetToolsUsed(res.ToolsUsed).
		SetCompletedAt(time.Now()).
		Exec(ctx)
	return errors.Wrap(err, "complete job")
}

// Fail records an investigation failure and transitions the job to StatusError.
func (s *Store) Fail(ctx context.Context, id uuid.UUID, cause error) error {
	err := s.db.InvestigationJob.UpdateOneID(id).
		SetStatus(string(StatusError)).
		SetErrorMessage(cause.Error()).
		SetCompletedAt(time.Now()).
		Exec(ctx)
	return errors.Wrap(err, "fail job")
}

// Get returns the job with the given ID, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Job, error) {
	m, err := s.db.InvestigationJob.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return Job{}, ErrNotFound
		}
		return Job{}, errors.Wrap(err, "get job")
	}
	return toJob(m)
}

// ReapStale marks any job still pending or running as failed. Call once at
// startup: a job left pending or running only happens if the previous
// ssagent process crashed or was killed mid-investigation, and without this
// the row would sit unresolved forever, hanging any client still polling it.
func (s *Store) ReapStale(ctx context.Context) (int, error) {
	n, err := s.db.InvestigationJob.Update().
		Where(investigationjob.StatusIn(string(StatusPending), string(StatusRunning))).
		SetStatus(string(StatusError)).
		SetErrorMessage("interrupted by ssagent restart").
		SetCompletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return n, errors.Wrap(err, "reap stale jobs")
	}
	return n, nil
}

func toJob(m *ent.InvestigationJob) (Job, error) {
	report, err := mapToReport(m.Report)
	if err != nil {
		return Job{}, errors.Wrap(err, "decode report")
	}
	return Job{
		ID:           m.ID,
		Status:       Status(m.Status),
		Report:       report,
		Iterations:   m.Iterations,
		ToolsUsed:    m.ToolsUsed,
		ErrorMessage: m.ErrorMessage,
		CreatedAt:    m.CreatedAt,
		StartedAt:    m.StartedAt,
		CompletedAt:  m.CompletedAt,
	}, nil
}

func reportToMap(r agent.Report) (map[string]any, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func mapToReport(m map[string]any) (agent.Report, error) {
	if len(m) == 0 {
		return agent.Report{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return agent.Report{}, err
	}
	var out agent.Report
	if err := json.Unmarshal(b, &out); err != nil {
		return agent.Report{}, err
	}
	return out, nil
}
