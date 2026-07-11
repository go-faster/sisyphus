package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/index"
)

func TestFetcher_Fetch(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Internal", "secret")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	f := newTestFetcher(t, config.FetchSite{
		Name:        "test",
		URLPatterns: []string{srv.URL + "/**"},
		Credentials: config.FetchCredentials{Type: "bearer", Token: "tok"},
	})

	resp, err := f.Fetch(context.Background(), index.FetchRequest{
		URL:     srv.URL + "/doc",
		Headers: map[string]string{"Authorization": "caller"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "hello", resp.Body)
	require.Equal(t, "test", resp.FromSite)
	require.False(t, resp.Truncated)
	require.Equal(t, "text/plain", resp.Headers["Content-Type"])
	require.NotContains(t, resp.Headers, "X-Internal")
	require.Equal(t, "Bearer tok", gotAuth)
}

func TestFetcher_RejectsURLOutsideAllowlist(t *testing.T) {
	f := newTestFetcher(t, config.FetchSite{Name: "test", URLPatterns: []string{"https://example.com/**"}})
	_, err := f.Fetch(context.Background(), index.FetchRequest{URL: "https://evil.example/doc"})
	require.ErrorIs(t, err, index.ErrURLNotAllowed)
}

func TestFetcher_RejectsNonHTTPURL(t *testing.T) {
	f := newTestFetcher(t, config.FetchSite{Name: "test", URLPatterns: []string{"https://example.com/**"}})
	_, err := f.Fetch(context.Background(), index.FetchRequest{URL: "file:///etc/passwd"})
	require.ErrorIs(t, err, index.ErrURLNotAllowed)
}

func TestFetcher_RejectsDisallowedMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	f := newTestFetcher(t, config.FetchSite{Name: "test", URLPatterns: []string{srv.URL + "/**"}})
	_, err := f.Fetch(context.Background(), index.FetchRequest{URL: srv.URL + "/doc", Method: http.MethodPost})
	require.ErrorIs(t, err, index.ErrFetchMethodNotAllowed)
}

func TestFetcher_TruncatesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abcdef"))
	}))
	defer srv.Close()

	f := newTestFetcher(t, config.FetchSite{Name: "test", URLPatterns: []string{srv.URL + "/**"}, MaxBytes: 3})
	resp, err := f.Fetch(context.Background(), index.FetchRequest{URL: srv.URL + "/doc"})
	require.NoError(t, err)
	require.Equal(t, "abc", resp.Body)
	require.True(t, resp.Truncated)
}

func TestFetcher_Credentials(t *testing.T) {
	tests := []struct {
		name       string
		creds      config.FetchCredentials
		headerName string
		wantHeader string
	}{
		{name: "basic", creds: config.FetchCredentials{Type: "basic", Username: "u", Password: "p"}, headerName: "Authorization", wantHeader: "Basic dTpw"},
		{name: "header", creds: config.FetchCredentials{Type: "header", Header: "X-API-Key", Token: "key"}, headerName: "X-API-Key", wantHeader: "key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get(tt.headerName)
			}))
			defer srv.Close()

			f := newTestFetcher(t, config.FetchSite{Name: "test", URLPatterns: []string{srv.URL + "/**"}, Credentials: tt.creds})
			_, err := f.Fetch(context.Background(), index.FetchRequest{URL: srv.URL + "/doc"})
			require.NoError(t, err)
			require.Equal(t, tt.wantHeader, got)
		})
	}
}

func TestProxyURL(t *testing.T) {
	proxies := config.ProxyConfig{
		Fetch:      "http://fetch-proxy:8080",
		Git:        "http://git-proxy:8080",
		GitLab:     "http://gitlab-proxy:8080",
		Jira:       "http://jira-proxy:8080",
		Ollama:     "http://ollama-proxy:8080",
		OpenRouter: "http://openrouter-proxy:8080",
	}

	tests := []struct {
		name      string
		proxyName string
		want      string
	}{
		{name: "empty", proxyName: "", want: ""},
		{name: "fetch", proxyName: "fetch", want: "http://fetch-proxy:8080"},
		{name: "git", proxyName: "git", want: "http://git-proxy:8080"},
		{name: "gitlab", proxyName: "gitlab", want: "http://gitlab-proxy:8080"},
		{name: "jira", proxyName: "jira", want: "http://jira-proxy:8080"},
		{name: "ollama", proxyName: "ollama", want: "http://ollama-proxy:8080"},
		{name: "openrouter", proxyName: "openrouter", want: "http://openrouter-proxy:8080"},
		{name: "fetch_uppercase", proxyName: "FETCH", want: "http://fetch-proxy:8080"},
		{name: "fetch_mixed_case", proxyName: "FeTcH", want: "http://fetch-proxy:8080"},
		{name: "unknown", proxyName: "unknown", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyURL(proxies, tt.proxyName)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFetcher_FetchWithFetchProxyFromConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	f := newTestFetcher(t, config.FetchSite{
		Name:        "test",
		URLPatterns: []string{srv.URL + "/**"},
		Proxy:       "fetch",
	})

	resp, err := f.Fetch(context.Background(), index.FetchRequest{URL: srv.URL + "/doc"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "hello", resp.Body)
}

func newTestFetcher(t *testing.T, site config.FetchSite) *Fetcher {
	t.Helper()
	if site.Timeout == 0 {
		site.Timeout = time.Second
	}
	f, err := New(context.Background(), config.FetchConfig{Sites: []config.FetchSite{site}}, config.ProxyConfig{}, Options{})
	require.NoError(t, err)
	return f
}
