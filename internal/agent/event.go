package agent

import (
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/index"
)

// ReportPayloadVersion is [ReportPayload]'s schema version. See
// [github.com/go-faster/sisyphus/internal/ingest/gitlab.MRPayloadVersion] for
// when to bump one.
const ReportPayloadVersion = 1

// ReportPayload is the source-typed body of an [event.TypeInvestigationCompleted]
// event: the finished report, plus what triggered it. A destination that wants
// more than the envelope's title decodes this.
type ReportPayload struct {
	JobID uuid.UUID `json:"job_id"`
	// TriggerEventID is the Event.ID that caused the investigation, so a
	// destination can correlate the report with the alert it came from.
	TriggerEventID string       `json:"trigger_event_id"`
	Verdict        Verdict      `json:"verdict"`
	Problem        string       `json:"problem"`
	Findings       string       `json:"findings"`
	Actions        []string     `json:"actions,omitempty"`
	Links          []index.Link `json:"links,omitempty"`
}

// EventFromReport builds the canonical event for a finished investigation.
//
// Its ID is derived from the job, not from the moment it finished: a job is
// already deduped by its idempotency key, so one investigation is one
// occurrence no matter how many times a destination is re-delivered it.
func EventFromReport(jobID uuid.UUID, trigger event.Event, report Report, completedAt time.Time) (event.Event, error) {
	e := event.Event{
		ID:     "investigation:" + jobID.String(),
		Source: event.SourceAgent,
		Type:   event.TypeInvestigationCompleted,
		Subject: event.Ref{
			ID:    jobID.String(),
			URL:   trigger.Subject.URL,
			Title: reportTitle(trigger, report),
		},
		// The investigation inherits the urgency of what triggered it: a
		// report on a critical alert is itself critical.
		Severity:   trigger.Severity,
		OccurredAt: completedAt,
		Attributes: trigger.Attributes,
	}
	e, err := e.WithPayload(ReportPayloadVersion, ReportPayload{
		JobID:          jobID,
		TriggerEventID: trigger.ID,
		Verdict:        report.Verdict,
		Problem:        report.Problem,
		Findings:       report.Findings,
		Actions:        report.Actions,
		Links:          report.Links,
	})
	if err != nil {
		return event.Event{}, errors.Wrap(err, "encode report payload")
	}
	return e, nil
}

// reportTitle names the investigation by what triggered it, falling back to
// the problem the agent decided it was looking at.
func reportTitle(trigger event.Event, report Report) string {
	if t := strings.TrimSpace(trigger.Subject.Title); t != "" {
		return t
	}
	if p := strings.TrimSpace(report.Problem); p != "" {
		return p
	}
	return "investigation"
}
