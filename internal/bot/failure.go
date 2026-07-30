package bot

import (
	"context"

	"github.com/go-faster/sdk/zctx"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// replyFailure logs err with a correlation id and tells the user only that the
// command failed, plus that id. It is called from dispatch alone — a handler
// signals failure by returning an error.
//
// A raw err.Error() in a chat is both useless to the reader and a leak: these
// errors come from ssapi and carry DSNs, internal hostnames, constraint names
// and identities of other users. The correlation id is the OTel trace id when
// the command is traced, so a report of "it failed, trace_id=…" lands directly
// on the span; without a real tracer it is a fresh random id, which still ties
// the chat message to exactly one log line.
func (b *Bot) replyFailure(ctx context.Context, s messageSender, command string, err error) {
	ref := failureRef(ctx)
	zctx.From(ctx).Error("command failed",
		zap.String("command", command), zap.Error(err), zap.String("ref", ref))
	b.sendTextReply(ctx, s, "Sorry, /"+command+" failed.\ntrace_id: "+ref)
}

func failureRef(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return uuid.NewString()
}
