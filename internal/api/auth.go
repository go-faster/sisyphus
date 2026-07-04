package api

import (
	"context"
	"crypto/subtle"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/oas"
)

// SecurityHandler enforces a single static bearer token on every request
// that declares the bearerAuth security requirement.
type SecurityHandler struct {
	token string
}

// NewSecurityHandler builds a SecurityHandler checking requests against token.
func NewSecurityHandler(token string) SecurityHandler {
	return SecurityHandler{token: token}
}

var _ oas.SecurityHandler = SecurityHandler{}

// HandleBearerAuth implements oas.SecurityHandler.
func (h SecurityHandler) HandleBearerAuth(ctx context.Context, _ oas.OperationName, t oas.BearerAuth) (context.Context, error) {
	if subtle.ConstantTimeCompare([]byte(t.Token), []byte(h.token)) != 1 {
		return ctx, errors.New("invalid bearer token")
	}
	return ctx, nil
}
