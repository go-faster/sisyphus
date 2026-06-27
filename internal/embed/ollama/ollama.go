// Package ollama implements index.Embedder backed by an Ollama server.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-faster/errors"

	"github.com/go-faster/scpbot/internal/index"
)

// Embedder is an index.Embedder implementation backed by an Ollama server.
type Embedder struct {
	baseURL    string
	model      string
	httpClient *http.Client
	dim        int
}

// embedRequest is the JSON request body for Ollama's embeddings API.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the JSON response from Ollama's embeddings API.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Option is a functional option for configuring Embedder.
type Option func(*Embedder)

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(client *http.Client) Option {
	return func(e *Embedder) {
		e.httpClient = client
	}
}

// WithDim sets the dimension of the embeddings. Defaults to 1024.
func WithDim(dim int) Option {
	return func(e *Embedder) {
		e.dim = dim
	}
}

// New creates a new Ollama embedder.
func New(baseURL, model string, opts ...Option) *Embedder {
	e := &Embedder{
		baseURL:    baseURL,
		model:      model,
		httpClient: http.DefaultClient,
		dim:        1024, // default for bge-m3
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Embed produces embedding vectors for the given texts.
// It returns one []float32 per input, in order.
// If texts is empty, it returns (nil, nil).
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Build the request.
	reqBody := embedRequest{
		Model: e.model,
		Input: texts,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, errors.Wrap(err, "marshal request")
	}

	// Create the HTTP request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "create request")
	}
	req.Header.Set("Content-Type", "application/json")

	// Execute the request.
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "do request")
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Handle non-2xx status codes.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a snippet of the body for error reporting.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, errors.Wrapf(nil, "ollama returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse the response.
	var respData embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
		return nil, errors.Wrap(err, "decode response")
	}

	// Validate the response length matches the input length.
	if len(respData.Embeddings) != len(texts) {
		return nil, errors.Wrapf(nil, "ollama returned %d embeddings but %d were requested", len(respData.Embeddings), len(texts))
	}

	return respData.Embeddings, nil
}

// Dim returns the dimension of the embeddings.
func (e *Embedder) Dim() int {
	return e.dim
}

// Verify that Embedder implements index.Embedder.
var _ index.Embedder = (*Embedder)(nil)
