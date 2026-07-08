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
		reqBody      interface{}
		authHeader   string
		setupInv     func() *fakeInvestigator
		expectedCode int
		expectedBody interface{}
	}{
		{
			name:       "happy path",
			reqMethod:  http.MethodPost,
			reqBody:    InvestigateRequest{Description: "test error"},
			authHeader: "Bearer secret",
			setupInv: func() *fakeInvestigator {
				return &fakeInvestigator{
					res: agent.Result{Report: "all good", Iterations: 2, ToolsUsed: 1},
				}
			},
			expectedCode: http.StatusOK,
			expectedBody: map[string]interface{}{
				"report":     "all good",
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
			handler := mcpserver.BearerAuthMiddleware("secret")(handleInvestigate(inv, 5*time.Second, logger))

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
				var got map[string]interface{}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				require.Equal(t, tt.expectedBody, got)
			}
		})
	}
}
