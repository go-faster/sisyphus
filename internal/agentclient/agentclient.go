// Package agentclient is an HTTP client for cmd/ssagent's /investigate endpoint.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-faster/errors"
	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/agent"
)

// Client wraps HTTP requests to ssagent.
type Client struct {
	http         *http.Client
	url          string
	token        string
	pollInterval time.Duration
	maxWait      time.Duration
}

// Options configures a new Client.
type Options struct {
	URL   string
	Token string

	// PollInterval is how often Investigate polls ssagent for a submitted
	// job's result. Defaults to 3s.
	PollInterval time.Duration
	// MaxWait bounds how long Investigate waits for a job to finish,
	// independent of any deadline on the ctx passed to Investigate (the
	// caller — internal/bot's investigateAsync goroutine — runs on a
	// context with no deadline of its own). Defaults to 20 minutes.
	MaxWait time.Duration
}

func (opts *Options) setDefaults() {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 3 * time.Second
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = 20 * time.Minute
	}
}

// New creates a new Client.
func New(opts Options) *Client {
	opts.setDefaults()
	return &Client{
		// This is the timeout for a single HTTP round trip (submit or one
		// poll), not for the whole investigation — Investigate itself no
		// longer holds one request open for the entire run, so it's bounded
		// by MaxWait instead.
		http:         &http.Client{Timeout: 30 * time.Second},
		url:          opts.URL,
		token:        opts.Token,
		pollInterval: opts.PollInterval,
		maxWait:      opts.MaxWait,
	}
}

// submitResponse is ssagent's POST /investigate response body
// (cmd/ssagent.InvestigateAcceptedResponse).
type submitResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// jobResponse is ssagent's GET /investigate/{id} response body
// (cmd/ssagent.InvestigateJobResponse).
type jobResponse struct {
	JobID      string        `json:"job_id"`
	Status     string        `json:"status"`
	Problem    string        `json:"problem,omitempty"`
	Steps      []string      `json:"steps,omitempty"`
	Verdict    agent.Verdict `json:"verdict,omitempty"`
	Findings   string        `json:"findings,omitempty"`
	Sources    []string      `json:"sources,omitempty"`
	Actions    []string      `json:"actions,omitempty"`
	Iterations int           `json:"iterations,omitempty"`
	ToolsUsed  int           `json:"tools_used,omitempty"`
	Error      string        `json:"error,omitempty"`
}

func (r jobResponse) report() agent.Report {
	return agent.Report{
		Problem:  r.Problem,
		Steps:    r.Steps,
		Verdict:  r.Verdict,
		Findings: r.Findings,
		Sources:  r.Sources,
		Actions:  r.Actions,
	}
}

// Investigate submits a /investigate job and polls until it finishes.
// Submission carries a fresh idempotency key and is retried a few times on
// transport failure — since a retry reuses the same key, ssagent returns the
// original job instead of starting a duplicate investigation even if an
// earlier attempt's response never made it back. Polling then survives any
// number of dropped connections on its own: each poll is a short, independent
// request against a persisted job, so a network blip only delays the result,
// it never loses it.
func (c *Client) Investigate(ctx context.Context, description string) (agent.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, c.maxWait)
	defer cancel()

	jobID, err := c.submit(ctx, description)
	if err != nil {
		return agent.Report{}, errors.Wrap(err, "submit investigation")
	}

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		job, err := c.poll(ctx, jobID)
		if err != nil {
			return agent.Report{}, errors.Wrap(err, "poll investigation")
		}
		switch job.Status {
		case "done":
			return job.report(), nil
		case "error":
			return agent.Report{}, errors.Errorf("investigation failed: %s", job.Error)
		}

		select {
		case <-ctx.Done():
			return agent.Report{}, errors.Wrap(ctx.Err(), "wait for investigation")
		case <-ticker.C:
		}
	}
}

// submitAttempts bounds retries of the initial submission on transport
// failure; a retry is safe because it reuses the same idempotency key.
const submitAttempts = 3

func (c *Client) submit(ctx context.Context, description string) (string, error) {
	key := uuid.NewString()
	reqBody := map[string]string{"description": description, "idempotency_key": key}

	var lastErr error
	for attempt := range submitAttempts {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		var resp submitResponse
		if err := c.doJSON(ctx, http.MethodPost, "/investigate", reqBody, &resp); err != nil {
			lastErr = err
			continue
		}
		return resp.JobID, nil
	}
	return "", lastErr
}

func (c *Client) poll(ctx context.Context, jobID string) (jobResponse, error) {
	var resp jobResponse
	err := c.doJSON(ctx, http.MethodGet, "/investigate/"+jobID, nil, &resp)
	return resp, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return errors.Wrap(err, "marshal request")
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url+path, reqBody)
	if err != nil {
		return errors.Wrap(err, "new request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(res.Body)
		return errors.Errorf("unexpected status %d: %s", res.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return errors.Wrap(err, "decode response")
		}
	}
	return nil
}

// CheckHealth checks ssagent's readiness, not just liveness: /readyz
// (internal/mcpserver.ReadinessHandler) actually verifies the MCP gateway
// backend is reachable, whereas /healthz always returns 200 as long as the
// process is up. Using /healthz here would report ssagent healthy even when
// its MCP backend — and therefore /investigate — is down.
func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/readyz", http.NoBody)
	if err != nil {
		return errors.Wrap(err, "new request")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return errors.Errorf("unexpected status %d", res.StatusCode)
	}
	return nil
}
