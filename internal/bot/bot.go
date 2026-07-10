// Package bot implements the Telegram bot that serves the /context command
// over MTProto via gotd (plan §10, §14).
package bot

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/gotd/log/logzap"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/telemetry"
)

// helpText is sent in reply to /start and /help.
const helpText = `Available commands:
/context <question> — search indexed knowledge and answer a question
/investigate <description> — run an on-demand investigation
/help — show this message`

const defaultAnswerTimeout = time.Minute

// Retriever is the minimal retrieval interface Bot needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

// QueryAnswerer answers a query while preserving query controls.
type QueryAnswerer interface {
	AnswerQuery(ctx context.Context, q index.Query, results []index.Result) (string, error)
}

// RichQueryAnswerer answers a query and returns a structured answer including
// actionable link buttons. Detected via type assertion; the bot falls back to
// QueryAnswerer when it is absent.
type RichQueryAnswerer interface {
	AnswerQueryRich(ctx context.Context, q index.Query, results []index.Result) (index.Answer, error)
}

// Investigator is the interface for running on-demand investigations.
type Investigator interface {
	Investigate(ctx context.Context, description string) (agent.Report, error)
}

// BotCredentials contains the credentials needed to run the bot.
type BotCredentials struct {
	AppID      int
	AppHash    string
	BotToken   string
	SessionDir string
}

// Bot serves /context over a Telegram bot session.
type Bot struct {
	cred   BotCredentials
	silent bool

	retriever         Retriever
	answerer          index.Answerer
	queryAnswerer     QueryAnswerer
	richQueryAnswerer RichQueryAnswerer
	investigator      Investigator

	tp            trace.TracerProvider
	mp            metric.MeterProvider
	tracer        trace.Tracer
	metrics       *botMetrics
	logger        *zap.Logger
	answerTimeout time.Duration

	allowedChats map[int64]struct{}
	allowedUsers map[int64]struct{}
}

// BotOptions configures the bot.
type BotOptions struct {
	// Silent disables actual sending of messages, useful for testing.
	Silent bool

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Logger         *zap.Logger
	AllowedChats   []int64
	AllowedUserIDs []int64
	Investigator   Investigator
	AnswerTimeout  time.Duration
}

func (opts *BotOptions) setDefaults() {
	if opts.TracerProvider == nil {
		opts.TracerProvider = otel.GetTracerProvider()
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	if opts.Logger == nil {
		opts.Logger = zap.L()
	}
	if opts.AnswerTimeout == 0 {
		opts.AnswerTimeout = defaultAnswerTimeout
	}
}

// New builds a Bot.
func New(_ context.Context, r Retriever, a index.Answerer, cred BotCredentials, opts BotOptions) *Bot {
	opts.setDefaults()
	tp := opts.TracerProvider
	mp := opts.MeterProvider
	m, _ := newBotMetrics(mp)

	// Build allowlist maps
	allowedChats := make(map[int64]struct{})
	for _, chatID := range opts.AllowedChats {
		allowedChats[chatID] = struct{}{}
	}
	allowedUsers := make(map[int64]struct{})
	for _, userID := range opts.AllowedUserIDs {
		allowedUsers[userID] = struct{}{}
	}

	if len(allowedChats) == 0 && len(allowedUsers) == 0 {
		opts.Logger.Warn("telegram bot: no allowlist configured, will not respond to anyone")
	}
	var queryAnswerer QueryAnswerer
	if qa, ok := a.(QueryAnswerer); ok {
		queryAnswerer = qa
	}
	var richQueryAnswerer RichQueryAnswerer
	if rqa, ok := a.(RichQueryAnswerer); ok {
		richQueryAnswerer = rqa
	}

	return &Bot{
		cred:              cred,
		silent:            opts.Silent,
		retriever:         r,
		answerer:          a,
		queryAnswerer:     queryAnswerer,
		richQueryAnswerer: richQueryAnswerer,
		investigator:      opts.Investigator,
		tp:                tp,
		mp:                mp,
		tracer:            tp.Tracer("github.com/go-faster/sisyphus/bot"),
		logger:            opts.Logger,
		metrics:           m,
		allowedChats:      allowedChats,
		allowedUsers:      allowedUsers,
		answerTimeout:     opts.AnswerTimeout,
	}
}

// peerChatID extracts a chat ID from a tg.PeerClass.
func peerChatID(p tg.PeerClass) int64 {
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

// isAllowed checks if a chat/user combination is in the allowlist.
func (b *Bot) isAllowed(chatID, userID int64) bool {
	_, isChat := b.allowedChats[chatID]
	_, isUser := b.allowedUsers[userID]
	return isChat || isUser
}

// Run connects, authenticates as a bot, and serves updates until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	dispatcher := tg.NewUpdateDispatcher()
	client := telegram.NewClient(b.cred.AppID, b.cred.AppHash, telegram.Options{
		Logger:         logzap.New(b.logger.Named("td")),
		UpdateHandler:  telemetry.LogUpdates(dispatcher, b.logger),
		TracerProvider: b.tp,
		SessionStorage: &telegram.FileSessionStorage{Path: filepath.Join(b.cred.SessionDir, "bot.json")},
		Middlewares: []telegram.Middleware{
			telemetry.TDMiddleware(b.tp, b.mp),
		},
	})
	raw := tg.NewClient(client)
	sender := message.NewSender(raw)

	// runCtx is the bot's process-lifetime context (canceled on shutdown), as
	// opposed to the per-update ctx handed to the OnNewMessage callback below.
	// gotd's update manager processes updates one at a time on a single
	// goroutine (see telegram/updates.internalState.handleUpdates), so any
	// handler that blocks until it returns stalls every other chat's messages
	// behind it. /investigate can take minutes (an LLM tool-calling loop with
	// several MCP round-trips via ssagent), so it must not run inline: reply
	// with an immediate ack, then do the actual work in a goroutine rooted in
	// runCtx (which outlives this callback invocation) and deliver the report
	// as a follow-up message.
	runCtx := ctx

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}

		// Check allowlist before processing
		chatID := peerChatID(msg.PeerID)
		senderID := peerChatID(msg.FromID)
		if !b.isAllowed(chatID, senderID) {
			zctx.From(ctx).Debug("bot: ignoring message from non-allowlisted chat/user",
				zap.Int64("chat_id", chatID), zap.Int64("sender_id", senderID))
			return nil
		}

		lg := zctx.From(ctx)
		cmd, rest, ok := parseCommand(msg.Message)
		if !ok {
			return nil
		}
		if rest == "" && cmd != "start" && cmd != "help" {
			return nil
		}

		switch cmd {
		case "start":
			lg.Info("start command", zap.Int64("user_id", senderID))
			if !b.silent {
				answer := fmt.Sprintf("Your ID: %d\n\n%s", senderID, helpText)
				if _, err := sender.Reply(e, u).Text(ctx, answer); err != nil {
					return errors.Wrap(err, "reply")
				}
			}
		case "help":
			lg.Info("help command")
			if !b.silent {
				if _, err := sender.Reply(e, u).Text(ctx, helpText); err != nil {
					return errors.Wrap(err, "reply")
				}
			}
		case "context":
			lg.Info("context command", zap.String("query", rest))
			answer, err := b.handle(ctx, rest)
			if err != nil {
				lg.Error("handle context", zap.Error(err))
				answer = index.Answer{Text: "Sorry, something went wrong handling that request."}
			}
			lg.Info("replying", zap.String("answer", answer.Text), zap.Int("buttons", len(answer.Links)))
			if !b.silent {
				req := sender.Reply(e, u)
				if kb := linksMarkup(answer.Links); kb != nil {
					req = req.Markup(kb)
				}
				if _, err := req.StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
					return renderMarkdown(eb, answer.Text)
				})); err != nil {
					lg.Warn("styled context reply failed, falling back to plain text", zap.Error(err))
					if _, err := req.Text(ctx, answer.Text); err != nil {
						return errors.Wrap(err, "reply fallback")
					}
				}
			}
		case "investigate":
			lg.Info("investigate command", zap.String("description", rest))
			if b.investigator == nil {
				if !b.silent {
					if _, err := sender.Reply(e, u).Text(ctx, "Investigation capability is not configured."); err != nil {
						return errors.Wrap(err, "reply")
					}
				}
				return nil
			}
			if !b.silent {
				if _, err := sender.Reply(e, u).Text(ctx, "Investigating, this may take a few minutes. I'll follow up here."); err != nil {
					return errors.Wrap(err, "ack reply")
				}
			}
			go b.investigateAsync(runCtx, sender, e, u, rest)
		default:
			return nil
		}
		return nil
	})
	return client.Run(ctx, func(ctx context.Context) error {
		if _, err := client.Auth().Bot(ctx, b.cred.BotToken); err != nil {
			return errors.Wrap(err, "bot auth")
		}
		b.logger.Info("bot authenticated, serving /context, /investigate, /start, /help")
		<-ctx.Done()
		return ctx.Err()
	})
}

func (b *Bot) handle(ctx context.Context, query string) (index.Answer, error) {
	start := time.Now()
	ctx, span := b.tracer.Start(ctx, "bot.context",
		trace.WithAttributes(attribute.Int("query.length", len(query))),
	)
	if b.answerTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.answerTimeout)
		defer cancel()
	}
	var (
		resultCount int
		rerr        error
	)
	defer func() {
		if b.metrics != nil {
			b.metrics.recordContext(ctx, time.Since(start).Seconds(), resultCount, rerr)
		}
		span.SetAttributes(attribute.Int("results.count", resultCount))
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()
	// Prefer the structured (rich) path so we can surface source-link buttons.
	if b.richQueryAnswerer != nil {
		answer, err := b.richQueryAnswerer.AnswerQueryRich(ctx, index.Query{Text: query, Limit: 12}, nil)
		if err != nil {
			rerr = errors.Wrap(err, "answer query")
			return index.Answer{}, rerr
		}
		return answer, nil
	}
	if b.queryAnswerer != nil {
		answer, err := b.queryAnswerer.AnswerQuery(ctx, index.Query{Text: query, Limit: 12}, nil)
		if err != nil {
			rerr = errors.Wrap(err, "answer query")
			return index.Answer{}, rerr
		}
		return index.Answer{Text: answer}, nil
	}
	if b.retriever == nil || b.answerer == nil {
		rerr = errors.New("bot answerer is not configured")
		return index.Answer{}, rerr
	}

	results, err := b.retriever.Retrieve(ctx, index.Query{Text: query, Limit: 12})
	if err != nil {
		rerr = errors.Wrap(err, "retrieve")
		return index.Answer{}, rerr
	}
	resultCount = len(results)
	answer, err := b.answerer.Answer(ctx, query, results)
	if err != nil {
		rerr = errors.Wrap(err, "answer")
		return index.Answer{}, rerr
	}
	return index.Answer{Text: answer}, nil
}

// investigateAsync runs an investigation in the background and delivers the
// report as one or more follow-up replies (Telegram caps a single message at
// telegramMessageLimit characters), so the caller (the OnNewMessage dispatch
// loop) never blocks on it.
func (b *Bot) investigateAsync(ctx context.Context, sender *message.Sender, e tg.Entities, u *tg.UpdateNewMessage, description string) {
	report, err := b.handleInvestigate(ctx, description)
	if err != nil {
		b.logger.Error("handle investigate", zap.Error(err))
		if !b.silent {
			if _, err := sender.Reply(e, u).Text(ctx, "Sorry, investigation failed."); err != nil {
				b.logger.Error("investigate follow-up reply failed", zap.Error(err))
			}
		}
		return
	}
	b.logger.Info("investigate reply", zap.String("verdict", string(report.Verdict)))
	if b.silent {
		return
	}

	chunks := splitMarkdown(reportMarkdown(report), telegramMessageLimit)
	kb := linksMarkup(report.Links)
	for i, chunk := range chunks {
		md := chunk
		req := sender.Reply(e, u)
		// Attach the link buttons to the final message so they sit at the
		// bottom of the whole report.
		if kb != nil && i == len(chunks)-1 {
			req = req.Markup(kb)
		}
		if _, err := req.StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
			return renderMarkdown(eb, md)
		})); err != nil {
			b.logger.Error("investigate follow-up reply failed", zap.Error(err))
			return
		}
	}
}

func (b *Bot) handleInvestigate(ctx context.Context, description string) (agent.Report, error) {
	start := time.Now()
	ctx, span := b.tracer.Start(ctx, "bot.investigate",
		trace.WithAttributes(attribute.Int("description.length", len(description))),
	)
	var (
		report agent.Report
		rerr   error
	)
	defer func() {
		if b.metrics != nil {
			b.metrics.recordInvestigate(ctx, time.Since(start).Seconds(), string(report.Verdict), rerr)
		}
		span.SetAttributes(attribute.String("verdict", string(report.Verdict)))
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	report, err := b.investigator.Investigate(ctx, description)
	if err != nil {
		rerr = errors.Wrap(err, "investigate")
		return agent.Report{}, rerr
	}
	return report, nil
}
