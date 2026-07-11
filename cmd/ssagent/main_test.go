package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap/zaptest"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/mcpserver"
)

type fakeInvestigator struct {
	res agent.Result
	err error
}

func (f *fakeInvestigator) Investigate(ctx context.Context, description string) (agent.Result, error) {
	return f.res, f.err
}

func TestHandleInvestigate(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tests := []struct {
		name         string
		reqMethod    string
		reqBody      any
		authHeader   string
		setupInv     func() *fakeInvestigator
		expectedCode int
		expectedBody any
	}{
		{
			name:       "happy path",
			reqMethod:  http.MethodPost,
			reqBody:    InvestigateRequest{Description: "test error"},
			authHeader: "Bearer secret",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{
					res: agent.Result{
						Report:     agent.Report{Problem: "all good", Verdict: agent.VerdictSolved},
						Iterations: 2,
						ToolsUsed:  1,
					},
				}
			},
			expectedCode: http.StatusOK,
			expectedBody: map[string]any{
				"problem":    "all good",
				"verdict":    "solved",
				"iterations": float64(2),
				"tools_used": float64(1),
			},
		},
		{
			name:       "wrong method",
			reqMethod:  http.MethodGet,
			authHeader: "Bearer secret",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{}
			},
			expectedCode: http.StatusMethodNotAllowed,
		},
		{
			name:       "no auth",
			reqMethod:  http.MethodPost,
			reqBody:    InvestigateRequest{Description: "test error"},
			authHeader: "",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{}
			},
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:       "empty description",
			reqMethod:  http.MethodPost,
			reqBody:    InvestigateRequest{Description: ""},
			authHeader: "Bearer secret",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{}
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:       "investigator error",
			reqMethod:  http.MethodPost,
			reqBody:    InvestigateRequest{Description: "test error"},
			authHeader: "Bearer secret",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{
					err: errors.New("boom"),
				}
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := tt.setupInv()
			handler := mcpserver.BearerAuthMiddleware("secret")(handleInvestigate(inv, 5*time.Second, 64*1024, nil, noop.NewTracerProvider().Tracer(""), nil, logger))

			var body []byte
			if tt.reqBody != nil {
				body, _ = json.Marshal(tt.reqBody)
			}

			req := httptest.NewRequest(tt.reqMethod, "/investigate", bytes.NewReader(body))
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			require.Equal(t, tt.expectedCode, rec.Code)

			if tt.expectedCode == http.StatusOK {
				var got map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				require.Equal(t, tt.expectedBody, got)
			}
		})
	}
}

func TestHandleInvestigate_ConcurrencyLimit(t *testing.T) {
	logger := zaptest.NewLogger(t)
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // pre-fill so the next request is rejected immediately.

	inv := &fakeInvestigator{res: agent.Result{Report: agent.Report{Verdict: agent.VerdictSolved}}}
	handler := mcpserver.BearerAuthMiddleware("secret")(handleInvestigate(inv, 5*time.Second, 64*1024, sem, noop.NewTracerProvider().Tracer(""), nil, logger))

	body, _ := json.Marshal(InvestigateRequest{Description: "test"})
	req := httptest.NewRequest(http.MethodPost, "/investigate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestHandleInvestigate_BodyTooLarge(t *testing.T) {
	logger := zaptest.NewLogger(t)
	inv := &fakeInvestigator{res: agent.Result{Report: agent.Report{Verdict: agent.VerdictSolved}}}
	handler := mcpserver.BearerAuthMiddleware("secret")(handleInvestigate(inv, 5*time.Second, 16, nil, noop.NewTracerProvider().Tracer(""), nil, logger))

	body, _ := json.Marshal(InvestigateRequest{Description: "this description is definitely longer than sixteen bytes"})
	req := httptest.NewRequest(http.MethodPost, "/investigate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
