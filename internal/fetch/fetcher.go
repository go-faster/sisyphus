package fetch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/netclient"
)

const (
	defaultMaxBytes = int64(1 << 20)
	defaultTimeout  = 30 * time.Second
)

// Options configures Fetcher construction.
type Options struct {
	Logger         *zap.Logger
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (o *Options) setDefaults() {
	if o.Logger == nil {
		o.Logger = zap.NewNop()
	}
	if o.TracerProvider == nil {
		o.TracerProvider = otel.GetTracerProvider()
	}
	if o.MeterProvider == nil {
		o.MeterProvider = otel.GetMeterProvider()
	}
}

// Fetcher implements index.URLFetcher using a per-site HTTP allowlist.
type Fetcher struct {
	sites  []siteConfig
	lg     *zap.Logger
	tracer trace.Tracer
}

var _ index.URLFetcher = (*Fetcher)(nil)

type siteConfig struct {
	name     string
	patterns []string
	methods  map[string]bool
	creds    credentialApplier
	maxBytes int64
	client   *http.Client
}

// New builds a Fetcher from resolved configuration.
func New(ctx context.Context, cfg config.FetchConfig, proxies config.ProxyConfig, opts Options) (*Fetcher, error) {
	opts.setDefaults()
	sites := make([]siteConfig, 0, len(cfg.Sites))
	for _, site := range cfg.Sites {
		methods, err := allowedMethods(site.Methods)
		if err != nil {
			return nil, errors.Wrap(err, "methods")
		}
		maxBytes := site.MaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultMaxBytes
		}
		timeout := site.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		client, err := netclient.HTTPClient(ctx, "fetch:"+site.Name, proxyURL(proxies, site.Proxy), netclient.HTTPClientOptions{
			TracerProvider: opts.TracerProvider,
			MeterProvider:  opts.MeterProvider,
			Timeout:        timeout,
		})
		if err != nil {
			return nil, errors.Wrap(err, "http client")
		}
		creds, err := newCredential(site.Credentials)
		if err != nil {
			return nil, errors.Wrap(err, "credentials")
		}
		sites = append(sites, siteConfig{
			name:     site.Name,
			patterns: append([]string(nil), site.URLPatterns...),
			methods:  methods,
			creds:    creds,
			maxBytes: maxBytes,
			client:   client,
		})
	}
	return &Fetcher{
		sites:  sites,
		lg:     opts.Logger,
		tracer: opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/fetch"),
	}, nil
}

func newCredential(c config.FetchCredentials) (credentialApplier, error) {
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "", "none":
		return noneCred{}, nil
	case "bearer":
		return bearerCred{token: c.Token}, nil
	case "basic":
		return basicCred{user: c.Username, pass: c.Password}, nil
	case "header":
		return headerCred{header: c.Header, value: c.Token}, nil
	default:
		return nil, errors.Errorf("unsupported credential type %q", c.Type)
	}
}

func proxyURL(proxies config.ProxyConfig, name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return ""
	case "git":
		return proxies.Git
	case "gitlab":
		return proxies.GitLab
	case "jira":
		return proxies.Jira
	case "ollama":
		return proxies.Ollama
	case "openrouter":
		return proxies.OpenRouter
	default:
		return ""
	}
}

// Fetch performs a whitelisted HTTP request.
func (f *Fetcher) Fetch(ctx context.Context, req index.FetchRequest) (_ index.FetchResponse, rerr error) {
	ctx, span := f.tracer.Start(ctx, "fetch.Fetch")
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
		}
		span.End()
	}()

	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return index.FetchResponse{}, index.ErrURLNotAllowed
	}

	site, ok := f.matchSite(req.URL)
	if !ok {
		return index.FetchResponse{}, index.ErrURLNotAllowed
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !site.methods[method] {
		return index.FetchResponse{FromSite: site.name}, index.ErrFetchMethodNotAllowed
	}

	var body io.Reader = http.NoBody
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return index.FetchResponse{FromSite: site.name}, errors.Wrap(err, "new request")
	}
	for key, value := range req.Headers {
		if strings.EqualFold(key, site.creds.headerName()) {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	site.creds.apply(httpReq)

	resp, err := site.client.Do(httpReq)
	if err != nil {
		return index.FetchResponse{FromSite: site.name}, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, site.maxBytes+1))
	if err != nil {
		return index.FetchResponse{StatusCode: resp.StatusCode, FromSite: site.name}, errors.Wrap(err, "read body")
	}
	truncated := int64(len(data)) > site.maxBytes
	if truncated {
		data = data[:site.maxBytes]
	}

	return index.FetchResponse{
		StatusCode: resp.StatusCode,
		Headers:    filteredHeaders(resp.Header),
		Body:       string(data),
		FromSite:   site.name,
		Truncated:  truncated,
	}, nil
}

func (f *Fetcher) matchSite(rawURL string) (siteConfig, bool) {
	for _, site := range f.sites {
		for _, pattern := range site.patterns {
			if matchPattern(pattern, rawURL) {
				return site, true
			}
		}
	}
	return siteConfig{}, false
}

func filteredHeaders(h http.Header) map[string]string {
	out := make(map[string]string, 4)
	for _, key := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified"} {
		if value := h.Get(key); value != "" {
			out[key] = value
		}
	}
	return out
}
