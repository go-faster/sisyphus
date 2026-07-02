// Package bot implements the Telegram bot that serves the /context command
// over MTProto via gotd (plan §10, §14).
package bot

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/gotd/log/logzap"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
	"github.com/go-faster/scpbot/internal/telemetry"
	"github.com/go-faster/scpbot/internal/wire"
)

// Retriever is the retrieval interface (alias to wire.Retriever).
type Retriever = wire.Retriever

// Config configures the bot.
type Config struct {
	AppID      int
	AppHash    string
	BotToken   string
	SessionDir string
}

// Bot serves /context over a Telegram bot session.
type Bot struct {
	cfg       Config
	retriever Retriever
	answerer  index.Answerer
	tp        trace.TracerProvider
	tracer    trace.Tracer
	m         *botMetrics
}

// New builds a Bot.
func New(_ context.Context, cfg Config, r Retriever, a index.Answerer, tp trace.TracerProvider, mp metric.MeterProvider) *Bot {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	m, _ := newBotMetrics(mp)
	return &Bot{
		cfg:       cfg,
		retriever: r,
		answerer:  a,
		tp:        tp,
		tracer:    tp.Tracer("github.com/go-faster/scpbot/bot"),
		m:         m,
	}
}

// Run connects, authenticates as a bot, and serves updates until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	dispatcher := tg.NewUpdateDispatcher()
	client := telegram.NewClient(b.cfg.AppID, b.cfg.AppHash, telegram.Options{
		Logger:         logzap.New(zctx.From(ctx).Named("td")),
		UpdateHandler:  dispatcher,
		TracerProvider: b.tp,
		SessionStorage: &telegram.FileSessionStorage{Path: filepath.Join(b.cfg.SessionDir, "bot.json")},
		Middlewares: []telegram.Middleware{
			telemetry.TDTracingMiddleware(b.tp),
		},
	})
	sender := message.NewSender(tg.NewClient(client))

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}
		query, ok := parseContextCommand(msg.Message)
		if !ok {
			return nil
		}
		zctx.From(ctx).Info("context command", zap.Int("query_len", len(query)))

		answer, err := b.handle(ctx, query)
		if err != nil {
			zctx.From(ctx).Error("handle context", zap.Error(err))
			answer = "Sorry, something went wrong handling that request."
		}
		if _, err := sender.Reply(e, u).Text(ctx, answer); err != nil {
			return errors.Wrap(err, "reply")
		}
		return nil
	})

	return client.Run(ctx, func(ctx context.Context) error {
		if _, err := client.Auth().Bot(ctx, b.cfg.BotToken); err != nil {
			return errors.Wrap(err, "bot auth")
		}
		zctx.From(ctx).Info("bot authenticated, serving /context")
		<-ctx.Done()
		return ctx.Err()
	})
}

func (b *Bot) handle(ctx context.Context, query string) (string, error) {
	start := time.Now()
	ctx, span := b.tracer.Start(ctx, "bot.context",
		trace.WithAttributes(attribute.Int("query.length", len(query))),
	)
	var (
		resultCount int
		rerr        error
	)
	defer func() {
		if b.m != nil {
			b.m.recordContext(ctx, time.Since(start).Seconds(), resultCount, rerr)
		}
		span.SetAttributes(attribute.Int("results.count", resultCount))
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	results, err := b.retriever.Retrieve(ctx, index.Query{Text: query, Limit: 12})
	if err != nil {
		rerr = errors.Wrap(err, "retrieve")
		return "", rerr
	}
	resultCount = len(results)
	answer, err := b.answerer.Answer(ctx, query, results)
	if err != nil {
		rerr = errors.Wrap(err, "answer")
		return "", rerr
	}
	return answer, nil
}

// parseContextCommand extracts the query from a "/context <query>" message.
// It tolerates a bot mention suffix, e.g. "/context@scpbot foo".
func parseContextCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/context") {
		return "", false
	}
	rest := strings.TrimPrefix(text, "/context")
	// Drop an optional @botname token.
	if strings.HasPrefix(rest, "@") {
		if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}
