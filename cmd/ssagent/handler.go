package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
)

type InvestigateRequest struct {
	Description string `json:"description"`
}

type InvestigateResponse struct {
	Report     string `json:"report"`
	Iterations int    `json:"iterations"`
	ToolsUsed  int    `json:"tools_used"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func sendError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: err.Error()})
}

func handleInvestigate(inv agent.Investigator, timeout time.Duration, logger *zap.Logger) http.HandlerFunc {
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

		res, err := inv.Investigate(ctx, req.Description)
		if err != nil {
			logger.Error("investigation failed", zap.Error(err))
			sendError(w, http.StatusInternalServerError, errors.Wrap(err, "investigate"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(InvestigateResponse{
			Report:     res.Report,
			Iterations: res.Iterations,
			ToolsUsed:  res.ToolsUsed,
		}); err != nil {
			logger.Error("encode response", zap.Error(err))
		}
	}
}
