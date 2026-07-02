package netclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// DoWithRetry executes an HTTP request using the provided client, with bounded retries for 429 and 5xx errors.
func DoWithRetry(ctx context.Context, op string, httpClient *http.Client, req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, errors.Wrap(err, "buffer request body for retry")
		}
		_ = req.Body.Close()
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		req.Body, _ = req.GetBody()
	}

	operation := func() (*http.Response, error) {
		if req.GetBody != nil {
			newBody, err := req.GetBody()
			if err != nil {
				return nil, backoff.Permanent(errors.Wrap(err, "get request body"))
			}
			req.Body = newBody
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, errors.Wrap(err, op) // retry on network error
		}

		switch resp.StatusCode {
		case http.StatusTooManyRequests,
			http.StatusConflict,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			_ = resp.Body.Close()
			return nil, errors.Errorf("%s status %d", op, resp.StatusCode) // retry, where applicable
		default:
			return resp, nil
		}
	}

	return backoff.Retry(ctx, operation,
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxTries(3),
		backoff.WithNotify(func(err error, d time.Duration) {
			zctx.From(ctx).Warn("retrying request",
				zap.String("op", op),
				zap.Error(err),
				zap.Duration("delay", d),
			)
		}),
	)
}
