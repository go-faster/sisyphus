package openrouter

import (
	"encoding/json"
	"fmt"
)

// UpstreamError is returned when OpenRouter reports an upstream/provider
// failure inside an otherwise-successful (HTTP 200) response body. The
// openai-go SDK treats such a response as success — it only retries and errors
// on non-2xx statuses — so a provider that fails mid-generation surfaces to the
// caller as a normal completion whose content is the error text. We detect it
// here and turn it into a real error instead.
//
// Per OpenRouter's error docs the failure appears in one of two shapes:
//   - a top-level {"error": {code, message}} object (standard error response,
//     usually with no choices), or
//   - embedded in the choice — {"choices":[{"finish_reason":"error","error":{…}}]}
//     — when the provider fails after generation has already started, which is
//     the HTTP-200 case.
type UpstreamError struct {
	Code    int
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("openrouter upstream error (code %d): %s", e.Code, e.Message)
	}
	return "openrouter upstream error: " + e.Message
}

type providerError struct {
	Code    json.Number `json:"code"`
	Message string      `json:"message"`
}

func (p *providerError) upstream() *UpstreamError {
	e := &UpstreamError{Message: p.Message}
	if n, err := p.Code.Int64(); err == nil {
		e.Code = int(n)
	}
	if e.Message == "" {
		e.Message = "provider reported an error"
	}
	return e
}

// upstreamError inspects a raw chat-completion response body for the OpenRouter
// error envelope and returns a non-nil *UpstreamError if the provider failed.
// It returns nil for a healthy response, an empty body, or a body it can't
// parse — never masking real content on a false match.
func upstreamError(raw string) *UpstreamError {
	if raw == "" {
		return nil
	}
	var body struct {
		Error   *providerError `json:"error"`
		Choices []struct {
			FinishReason string         `json:"finish_reason"`
			Error        *providerError `json:"error"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil
	}
	if body.Error != nil {
		return body.Error.upstream()
	}
	for _, ch := range body.Choices {
		if ch.Error != nil {
			return ch.Error.upstream()
		}
		if ch.FinishReason == "error" {
			return &UpstreamError{Message: `provider terminated with finish_reason "error"`}
		}
	}
	return nil
}
