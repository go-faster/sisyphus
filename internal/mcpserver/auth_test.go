package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerAuthMiddleware_MissingHeader(t *testing.T) {
	middleware := BearerAuthMiddleware("test-token")
	innerCalled := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/mcp", http.NoBody)
	handler.ServeHTTP(rec, req)

	require.False(t, innerCalled, "inner handler should not be called")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, `Bearer`, rec.Header().Get("WWW-Authenticate"))
}

func TestBearerAuthMiddleware_WrongToken(t *testing.T) {
	middleware := BearerAuthMiddleware("test-token")
	innerCalled := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/mcp", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong-token")
	handler.ServeHTTP(rec, req)

	require.False(t, innerCalled, "inner handler should not be called")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBearerAuthMiddleware_CorrectToken(t *testing.T) {
	middleware := BearerAuthMiddleware("test-token")
	innerCalled := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/mcp", http.NoBody)
	req.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(rec, req)

	require.True(t, innerCalled, "inner handler should be called")
	require.Equal(t, http.StatusOK, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	require.Equal(t, "success", string(body))
}

func TestBearerAuthMiddleware_MalformedBearer(t *testing.T) {
	middleware := BearerAuthMiddleware("test-token")
	innerCalled := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/mcp", http.NoBody)
	req.Header.Set("Authorization", "Basic dGVzdC10b2tlbjo=")
	handler.ServeHTTP(rec, req)

	require.False(t, innerCalled, "inner handler should not be called")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBearerAuthMiddleware_EmptyToken(t *testing.T) {
	middleware := BearerAuthMiddleware("test-token")
	innerCalled := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/mcp", http.NoBody)
	req.Header.Set("Authorization", "Bearer ")
	handler.ServeHTTP(rec, req)

	require.False(t, innerCalled, "inner handler should not be called")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
