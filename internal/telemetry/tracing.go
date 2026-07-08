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

// LogUpdates wraps a Telegram update handler and logs every incoming update at
// debug level before dispatch. It is useful for diagnosing whether Telegram is
// delivering a specific update class.
func LogUpdates(next telegram.UpdateHandler, lg *zap.Logger) telegram.UpdateHandler {
	return telegram.UpdateHandlerFunc(func(ctx context.Context, updates tg.UpdatesClass) error {
		if lg == nil {
			lg = zctx.From(ctx)
		}
		if lg.Core().Enabled(zap.DebugLevel) {
			logUpdateBatch(lg, updates)
		}
		return next.Handle(ctx, updates)
	})
}

func logUpdateBatch(lg *zap.Logger, updates tg.UpdatesClass) {
	if updates == nil {
		lg.Debug("telegram update", zap.String("updates_type", "<nil>"))
		return
	}

	inner := updateBatchItems(updates)
	lg.Debug("telegram updates",
		zap.String("updates_type", updates.TypeName()),
		zap.Uint32("updates_type_id", updates.TypeID()),
		zap.Int("updates_count", len(inner)),
	)
	for _, update := range inner {
		logUpdate(lg, update)
	}
}

func updateBatchItems(updates tg.UpdatesClass) []tg.UpdateClass {
	switch u := updates.(type) {
	case *tg.Updates:
		return u.Updates
	case *tg.UpdatesCombined:
		return u.Updates
	case *tg.UpdateShort:
		return []tg.UpdateClass{u.Update}
	default:
		return nil
	}
}

func logUpdate(lg *zap.Logger, update tg.UpdateClass) {
	if update == nil {
		lg.Debug("telegram update", zap.String("update_type", "<nil>"))
		return
	}
	fields := []zap.Field{
		zap.String("update_type", update.TypeName()),
		zap.Uint32("update_type_id", update.TypeID()),
	}
	switch u := update.(type) {
	case *tg.UpdateNewMessage:
		if msg, ok := u.Message.(*tg.Message); ok {
			fields = append(fields,
				zap.Int64("chat_id", peerID(msg.PeerID)),
				zap.Int64("sender_id", peerID(msg.FromID)),
				zap.Int("message_length", len(msg.Message)),
				zap.Bool("out", msg.Out),
			)
		}
	}
	lg.Debug("telegram update", fields...)
}

func peerID(p tg.PeerClass) int64 {
	if p == nil {
		return 0
	}
	switch peer := p.(type) {
	case *tg.PeerUser:
		return peer.UserID
	case *tg.PeerChat:
		return peer.ChatID
	case *tg.PeerChannel:
		return peer.ChannelID
	default:
		return 0
	}
}

// InjectLogger returns a telegram.Middleware that logs all updates using the provided logger.
func InjectLogger(next tg.Handler, lg *zap.Logger) tg.Handler {
	return func(ctx context.Context, entities tg.Entities, u tg.UpdateClass) error {
		lg.Debug("got update", zap.String("class", u.TypeName()))
		ctx = zctx.Base(ctx, lg)
		return next(ctx, entities, u)
	}
}
