package content

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/index"
)

// ChainResolver combines multiple resolvers, trying them in order.
type ChainResolver struct {
	resolvers []index.ContentResolver
	lg        *zap.Logger
	tracer    trace.Tracer
}

func NewChainResolver(resolvers []index.ContentResolver, opts Options) *ChainResolver {
	opts.setDefaults()
	return &ChainResolver{
		resolvers: resolvers,
		lg:        opts.Logger,
		tracer:    opts.TracerProvider.Tracer("github.com/go-faster/sisyphus/content"),
	}
}

func (c *ChainResolver) ResolveContent(ctx context.Context, req index.ContentRequest) (_ index.ContentResponse, rerr error) {
	ctx, span := c.tracer.Start(ctx, "content.ChainResolver.ResolveContent",
		trace.WithAttributes(
			attribute.String("repo", req.Repo),
			attribute.String("path", req.Path),
		),
	)
	defer func() {
		if rerr != nil {
			span.RecordError(rerr)
		}
		span.End()
	}()
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
