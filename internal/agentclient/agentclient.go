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
)

// Client wraps HTTP requests to ssagent.
type Client struct {
	http  *http.Client
	url   string
	token string
}

// Options configures a new Client.
type Options struct {
	URL   string
	Token string
}

// New creates a new Client.
func New(opts Options) *Client {
	return &Client{
		http:  &http.Client{Timeout: 5 * time.Minute},
		url:   opts.URL,
		token: opts.Token,
	}
}

// Investigate calls the ssagent /investigate endpoint.
func (c *Client) Investigate(ctx context.Context, description string) (string, error) {
	reqBody, err := json.Marshal(map[string]string{"description": description})
	if err != nil {
		return "", errors.Wrap(err, "marshal request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/investigate", bytes.NewReader(reqBody))
	if err != nil {
		return "", errors.Wrap(err, "new request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "do request")
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return "", errors.Errorf("unexpected status %d: %s", res.StatusCode, string(body))
	}

	var resp struct {
		Report string `json:"report"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return "", errors.Wrap(err, "decode response")
	}

	return resp.Report, nil
}

// CheckHealth checks the ssagent health endpoint.
func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/healthz", http.NoBody)
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
