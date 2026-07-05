package telemetry

import (
	"context"
	"fmt"

	"github.com/go-faster/sdk/zctx"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TDTracingMiddleware returns a telegram.Middleware that wraps MTProto API
// calls with OpenTelemetry spans.
func TDTracingMiddleware(tp trace.TracerProvider) telegram.Middleware {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	tracer := tp.Tracer("gotd/mtproto")
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			op := fmt.Sprintf("%T", input)
			ctx, span := tracer.Start(ctx, op,
				trace.WithAttributes(attribute.String("rpc.method", op)),
				trace.WithSpanKind(trace.SpanKindClient),
			)
			defer span.End()
			err := next.Invoke(ctx, input, output)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			return err
		}
	})
}

// InjectLogger returns a telegram.Middleware that logs all updates using the provided logger.
func InjectLogger(next tg.Handler, lg *zap.Logger) tg.Handler {
	return func(ctx context.Context, entities tg.Entities, u tg.UpdateClass) error {
		lg.Debug("got update", zap.String("class", u.TypeName()))
		ctx = zctx.Base(ctx, lg)
		return next(ctx, entities, u)
	}
}
