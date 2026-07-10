package content

import (
	"context"

	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// ChainResolver combines multiple resolvers, trying them in order.
type ChainResolver struct {
	resolvers []index.ContentResolver
	lg        *zap.Logger
}

func NewChainResolver(lg *zap.Logger, resolvers ...index.ContentResolver) *ChainResolver {
	return &ChainResolver{
		resolvers: resolvers,
		lg:        lg,
	}
}

func (c *ChainResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (index.ContentResponse, error) {
	for _, r := range c.resolvers {
		resp, err := r.ResolveContent(ctx, req)
		if err != nil {
			c.lg.Warn("Resolver returned error", zap.Error(err))
			continue
		}
		if resp.Found {
			return resp, nil
		}
	}
	return index.ContentResponse{Found: false}, nil
}
