package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
)

type InvestigateRequest struct {
	Description string `json:"description"`
}

type InvestigateResponse struct {
	Problem    string        `json:"problem"`
	Steps      []string      `json:"steps,omitempty"`
	Verdict    agent.Verdict `json:"verdict"`
	Findings   string        `json:"findings,omitempty"`
	Sources    []string      `json:"sources,omitempty"`
	Actions    []string      `json:"actions,omitempty"`
	Iterations int           `json:"iterations"`
	ToolsUsed  int           `json:"tools_used"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func sendError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: err.Error()})
}

func handleInvestigate(inv agent.Investigator, timeout time.Duration, tracer trace.Tracer, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
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

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		ctx, span := tracer.Start(ctx, "ssagent.investigate",
			trace.WithAttributes(attribute.Int("description.length", len(req.Description))),
		)
		defer span.End()

		res, err := inv.Investigate(ctx, req.Description)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			logger.Error("investigation failed", zap.Error(err))
			sendError(w, http.StatusInternalServerError, errors.Wrap(err, "investigate"))
			return
		}
		span.SetAttributes(
			attribute.String("verdict", string(res.Report.Verdict)),
			attribute.Int("iterations", res.Iterations),
			attribute.Int("tools_used", res.ToolsUsed),
			attribute.Int("report.chars", res.Report.CharLen()),
		)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(InvestigateResponse{
			Problem:    res.Report.Problem,
			Steps:      res.Report.Steps,
			Verdict:    res.Report.Verdict,
			Findings:   res.Report.Findings,
			Sources:    res.Report.Sources,
			Actions:    res.Report.Actions,
			Iterations: res.Iterations,
			ToolsUsed:  res.ToolsUsed,
		}); err != nil {
			logger.Error("encode response", zap.Error(err))
		}
	}
}
