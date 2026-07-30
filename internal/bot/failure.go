package bot

import (
	"context"

	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// replyFailure logs err with a correlation id and tells the user only that the
// action failed, plus that id.
//
// A raw err.Error() in a chat is both useless to the reader and a leak: these
// errors come from ssapi and carry DSNs, internal hostnames, constraint names
// and identities of other users. The correlation id is the OTel trace id when
// the command is traced, so a report of "it failed, trace_id=…" lands directly
// on the span; without a real tracer it is a fresh random id, which still ties
// the chat message to exactly one log line.
func (b *Bot) replyFailure(ctx context.Context, s messageSender, action string, err error) {
	ref := failureRef(ctx)
	zctx.From(ctx).Error(action+" failed", zap.Error(err), zap.String("ref", ref))
	b.sendTextReply(ctx, s, "Sorry, "+action+" failed.\ntrace_id: "+ref)
}

func failureRef(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return uuid.NewString()
}
