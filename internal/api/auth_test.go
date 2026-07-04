package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/oas"
)

func TestSecurityHandler_HandleBearerAuth(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		reqToken  string
		wantError bool
	}{
		{
			name:      "correct token",
			token:     "secret123",
			reqToken:  "secret123",
			wantError: false,
		},
		{
			name:      "wrong token",
			token:     "secret123",
			reqToken:  "wrong",
			wantError: true,
		},
		{
			name:      "empty request token with configured token",
			token:     "secret123",
			reqToken:  "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSecurityHandler(tt.token)
			ctx := context.Background()
			bearerAuth := oas.BearerAuth{Token: tt.reqToken}

			_, err := h.HandleBearerAuth(ctx, "TestOperation", bearerAuth)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestErrorHandler_SecurityError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectStatus   int
		expectResponse bool
	}{
		{
			name:           "security error returns 401",
			err:            &ogenerrors.SecurityError{Security: "BearerAuth"},
			expectStatus:   http.StatusUnauthorized,
			expectResponse: true,
		},
		{
			name:           "non-security error uses default handler",
			err:            &ogenerrors.DecodeRequestError{},
			expectStatus:   http.StatusBadRequest,
			expectResponse: false, // DefaultErrorHandler returns different response format
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx := context.Background()
			req := httptest.NewRequest("POST", "/search", http.NoBody)

			ErrorHandler(ctx, w, req, tt.err)

			assert.Equal(t, tt.expectStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			if tt.expectResponse {
				body, err := io.ReadAll(w.Body)
				require.NoError(t, err)
				var respBody map[string]any
				err = json.Unmarshal(body, &respBody)
				require.NoError(t, err)
				assert.Equal(t, "unauthorized", respBody["error_message"])
			}
		})
	}
}

func TestEndToEndSecurityAndErrors(t *testing.T) {
	stubRetriever := &captureRetriever{}
	handler := New(stubRetriever, stubAnswerer{}, "v1.0.0")

	// Create server with SecurityHandler and custom ErrorHandler
	secHandler := NewSecurityHandler("secret-token")
	server, err := oas.NewServer(handler, secHandler, oas.WithErrorHandler(ErrorHandler))
	require.NoError(t, err)

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	tests := []struct {
		name         string
		method       string
		path         string
		authHeader   string
		expectStatus int
		allowedPaths []string
	}{
		{
			name:         "GET /health without auth succeeds",
			method:       "GET",
			path:         "/health",
			authHeader:   "",
			expectStatus: http.StatusOK,
		},
		{
			name:         "POST /search without auth fails with 401",
			method:       "POST",
			path:         "/search",
			authHeader:   "",
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "POST /search with wrong auth fails with 401",
			method:       "POST",
			path:         "/search",
			authHeader:   "Bearer wrong-token",
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "POST /search with correct auth succeeds",
			method:       "POST",
			path:         "/search",
			authHeader:   "Bearer secret-token",
			expectStatus: http.StatusOK,
		},
		{
			name:         "POST /context without auth fails with 401",
			method:       "POST",
			path:         "/context",
			authHeader:   "",
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "POST /context with correct auth succeeds",
			method:       "POST",
			path:         "/context",
			authHeader:   "Bearer secret-token",
			expectStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.method == "POST" {
				// Build minimal valid request body
				switch tt.path {
				case "/search":
					reqBody, _ := json.Marshal(map[string]any{
						"query": "test",
					})
					body = bytes.NewReader(reqBody)
				case "/context":
					reqBody, _ := json.Marshal(map[string]any{
						"question": "test",
					})
					body = bytes.NewReader(reqBody)
				}
			}

			req, err := http.NewRequest(tt.method, httpServer.URL+tt.path, body)
			require.NoError(t, err)

			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.method == "POST" {
				req.Header.Set("Content-Type", "application/json")
			}

			resp, err := httpServer.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.expectStatus, resp.StatusCode)

			// For 401 responses, verify the JSON error message
			if resp.StatusCode == http.StatusUnauthorized {
				respBody, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				var data map[string]any
				err = json.Unmarshal(respBody, &data)
				require.NoError(t, err)
				assert.Equal(t, "unauthorized", data["error_message"])
			}
		})
	}
}
