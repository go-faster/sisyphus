package main

import (
	"context"
	"encoding/json"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/queue"
)

// investigateHandler runs one claimed investigation and records its outcome
// on the job row.
//
// It returns nil even when the investigation fails, because the failure is
// already persisted: acknowledging stops the queue from spending another
// LLM run on work that will fail the same way. Only a worker that dies
// before acknowledging leaves the delivery unsettled, and that is exactly the
// case a retry should cover.
func investigateHandler(store jobStore, inv agent.Investigator, tracer trace.Tracer, metrics *agentMetrics, lg *zap.Logger) queue.Handler {
	return func(ctx context.Context, d queue.Delivery) error {
		var p agentstore.Payload
		if err := json.Unmarshal(d.Payload, &p); err != nil {
			// Undecodable payload will never decode; let the queue retire it
			// rather than reclaiming it until its attempts run out.
			lg.Error("Decode investigation payload", zap.Stringer("job_id", d.ID), zap.Error(err))
			if failErr := store.Fail(ctx, d.ID, errors.Wrap(err, "decode payload")); failErr != nil {
				lg.Error("Persist job failure", zap.Stringer("job_id", d.ID), zap.Error(failErr))
			}
			return nil
		}

		runJob(ctx, store, inv, d.ID, p.Description, tracer, metrics, lg)
		return nil
	}
}
