package fetch

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/config"
)

func TestNewCredential_EmptyType(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: ""})
	require.NoError(t, err)

	// Should be noneCred
	_, ok := cred.(noneCred)
	require.True(t, ok)

	// Test apply is no-op
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "", req.Header.Get("Authorization"))

	// Test headerName is empty
	require.Equal(t, "", cred.headerName())
}

func TestNewCredential_NoneType(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: "none"})
	require.NoError(t, err)

	// Should be noneCred
	_, ok := cred.(noneCred)
	require.True(t, ok)

	// Test apply is no-op
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "", req.Header.Get("Authorization"))

	// Test headerName is empty
	require.Equal(t, "", cred.headerName())
}

func TestNewCredential_Bearer(t *testing.T) {
	token := "my-secret-token"
	cred, err := newCredential(config.FetchCredentials{Type: "bearer", Token: token})
	require.NoError(t, err)

	// Should be bearerCred
	bearerC, ok := cred.(bearerCred)
	require.True(t, ok)
	require.Equal(t, token, bearerC.token)

	// Test apply sets correct header
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "Bearer my-secret-token", req.Header.Get("Authorization"))

	// Test headerName
	require.Equal(t, "Authorization", cred.headerName())
}

func TestNewCredential_BearerCaseSensitive(t *testing.T) {
	token := "token123"
	cred, err := newCredential(config.FetchCredentials{Type: "Bearer", Token: token})
	require.NoError(t, err)

	// Should be bearerCred even with uppercase type
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "Bearer token123", req.Header.Get("Authorization"))
}

func TestNewCredential_BearerWhitespace(t *testing.T) {
	token := "token456"
	cred, err := newCredential(config.FetchCredentials{Type: "  bearer  ", Token: token})
	require.NoError(t, err)

	// Should be bearerCred even with whitespace
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "Bearer token456", req.Header.Get("Authorization"))
}

func TestNewCredential_Basic(t *testing.T) {
	username := "user"
	password := "pass"
	cred, err := newCredential(config.FetchCredentials{Type: "basic", Username: username, Password: password})
	require.NoError(t, err)

	// Should be basicCred
	basicC, ok := cred.(basicCred)
	require.True(t, ok)
	require.Equal(t, username, basicC.user)
	require.Equal(t, password, basicC.pass)

	// Test apply sets correct header
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	authHeader := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authHeader, "Basic "))

	// Decode and verify
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	require.NoError(t, err)
	require.Equal(t, "user:pass", string(decoded))

	// Test headerName
	require.Equal(t, "Authorization", cred.headerName())
}

func TestNewCredential_BasicCaseSensitive(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: "BASIC", Username: "u", Password: "p"})
	require.NoError(t, err)

	// Should be basicCred even with uppercase
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	authHeader := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authHeader, "Basic "))
}

func TestNewCredential_BasicWhitespace(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: "  basic  ", Username: "user", Password: "pass"})
	require.NoError(t, err)

	// Should be basicCred even with whitespace
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	authHeader := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authHeader, "Basic "))
}

func TestNewCredential_Header(t *testing.T) {
	header := "X-Custom-Auth"
	value := "custom-value"
	cred, err := newCredential(config.FetchCredentials{Type: "header", Header: header, Token: value})
	require.NoError(t, err)

	// Should be headerCred
	hdrC, ok := cred.(headerCred)
	require.True(t, ok)
	require.Equal(t, header, hdrC.header)
	require.Equal(t, value, hdrC.value)

	// Test apply sets correct custom header
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, value, req.Header.Get(header))

	// Test headerName returns the custom header name
	require.Equal(t, header, cred.headerName())
}

func TestNewCredential_HeaderCaseSensitive(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: "HEADER", Header: "X-API-Key", Token: "key123"})
	require.NoError(t, err)

	// Should be headerCred even with uppercase type
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "key123", req.Header.Get("X-API-Key"))
}

func TestNewCredential_HeaderWhitespace(t *testing.T) {
	cred, err := newCredential(config.FetchCredentials{Type: "  header  ", Header: "X-Token", Token: "tok"})
	require.NoError(t, err)

	// Should be headerCred even with whitespace around type
	req := &http.Request{Header: make(http.Header)}
	cred.apply(req)
	require.Equal(t, "tok", req.Header.Get("X-Token"))
}

func TestNewCredential_UnsupportedType(t *testing.T) {
	_, err := newCredential(config.FetchCredentials{Type: "digest"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported credential type")
}

func TestNewCredential_UnknownType(t *testing.T) {
	_, err := newCredential(config.FetchCredentials{Type: "oauth2"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported credential type")
}

func TestNewCredential_InvalidTypeAfterNormalization(t *testing.T) {
	_, err := newCredential(config.FetchCredentials{Type: "  unknown  "})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported credential type")
}

func TestBearerCred_Apply(t *testing.T) {
	cred := bearerCred{token: "test-token"}
	req := &http.Request{Header: make(http.Header)}

	cred.apply(req)

	require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
}

func TestBearerCred_HeaderName(t *testing.T) {
	cred := bearerCred{}
	require.Equal(t, "Authorization", cred.headerName())
}

func TestBasicCred_Apply(t *testing.T) {
	cred := basicCred{user: "admin", pass: "secret"}
	req := &http.Request{Header: make(http.Header)}

	cred.apply(req)

	authHeader := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(authHeader, "Basic "))
	decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	require.Equal(t, "admin:secret", string(decoded))
}

func TestBasicCred_HeaderName(t *testing.T) {
	cred := basicCred{}
	require.Equal(t, "Authorization", cred.headerName())
}

func TestHeaderCred_Apply(t *testing.T) {
	cred := headerCred{header: "X-API-Token", value: "secret123"}
	req := &http.Request{Header: make(http.Header)}

	cred.apply(req)

	require.Equal(t, "secret123", req.Header.Get("X-API-Token"))
}

func TestHeaderCred_HeaderName(t *testing.T) {
	cred := headerCred{header: "X-Custom-Header"}
	require.Equal(t, "X-Custom-Header", cred.headerName())
}

func TestNoneCred_Apply(t *testing.T) {
	cred := noneCred{}
	req := &http.Request{Header: make(http.Header)}

	// Apply should be a no-op
	cred.apply(req)

	// No headers should be set
	require.Equal(t, 0, len(req.Header))
}

func TestNoneCred_HeaderName(t *testing.T) {
	cred := noneCred{}
	require.Equal(t, "", cred.headerName())
}

func TestCredentialApplier_MultipleApplies(t *testing.T) {
	// Test that applying a credential multiple times works correctly
	cred := bearerCred{token: "token"}
	req := &http.Request{Header: make(http.Header)}

	cred.apply(req)
	require.Equal(t, "Bearer token", req.Header.Get("Authorization"))

	cred.apply(req)
	// Should overwrite, not append
	require.Equal(t, "Bearer token", req.Header.Get("Authorization"))
}

func TestHeaderCred_ApplyDoesNotAffectOtherHeaders(t *testing.T) {
	cred := headerCred{header: "X-Custom", value: "val"}
	req := &http.Request{Header: make(http.Header)}
	req.Header.Set("X-Existing", "existing-value")

	cred.apply(req)

	require.Equal(t, "val", req.Header.Get("X-Custom"))
	require.Equal(t, "existing-value", req.Header.Get("X-Existing"))
}
