// Package bot implements the Telegram bot that serves the /context command
// over MTProto via gotd (plan §10, §14).
package bot

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/gotd/log/logzap"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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

const defaultAnswerTimeout = time.Minute

// Retriever is the minimal retrieval interface Bot needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
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

	retriever    Retriever
	answerer     index.Answerer
	investigator Investigator

	tp            trace.TracerProvider
	mp            metric.MeterProvider
	tracer        trace.Tracer
	metrics       *botMetrics
	logger        *zap.Logger
	answerTimeout time.Duration

	allowedChats map[int64]struct{}
	allowedUsers map[int64]struct{}

	commands *commandRegistry
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

	return &Bot{
		cred:          cred,
		silent:        opts.Silent,
		retriever:     r,
		answerer:      a,
		investigator:  opts.Investigator,
		tp:            tp,
		mp:            mp,
		tracer:        tp.Tracer("github.com/go-faster/sisyphus/internal/bot"),
		logger:        opts.Logger,
		metrics:       m,
		allowedChats:  allowedChats,
		allowedUsers:  allowedUsers,
		answerTimeout: opts.AnswerTimeout,
		commands:      newCommandRegistry(),
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

	b.commands = b.buildCommandRegistry(runCtx)

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok || msg.Out {
			return nil
		}

		chatID := peerChatID(msg.PeerID)
		senderID := peerChatID(msg.FromID)
		if !b.isAllowed(chatID, senderID) {
			zctx.From(ctx).Debug("bot: ignoring message from non-allowlisted chat/user",
				zap.Int64("chat_id", chatID), zap.Int64("sender_id", senderID))
			return nil
		}

		cmd, rest, ok := parseCommand(msg.Message)
		if !ok {
			return nil
		}
		c, ok := b.commands.lookup(cmd)
		if !ok {
			return nil
		}
		// Commands with a non-empty usage require arguments; silently ignore
		// bare invocations (preserves the old switch's behavior).
		if rest == "" && c.usage != "" {
			return nil
		}

		var s messageSender
		if !b.silent {
			s = newReplySender(sender, e, u)
		} else {
			s = silentSender{}
		}
		return c.handler(ctx, s, senderID, rest)
	})

	dispatcher.OnBotInlineQuery(func(ctx context.Context, _ tg.Entities, u *tg.UpdateBotInlineQuery) error {
		if !b.isAllowed(0, u.UserID) {
			zctx.From(ctx).Debug("bot: ignoring inline query from non-allowlisted user",
				zap.Int64("user_id", u.UserID))
			_, err := sender.Inline(u).SwitchPM("Start me to enable search", "start").Set(ctx)
			return err
		}

		query := parseInlineQuery(u.Query)
		if query == "" {
			_, err := sender.Inline(u).Set(ctx)
			return err
		}

		lg := zctx.From(ctx)
		lg.Info("inline search", zap.String("query", query))

		start := time.Now()
		ctx, span := b.tracer.Start(ctx, "bot.inline_search",
			trace.WithAttributes(attribute.Int("query.length", len(query))),
		)
		var (
			resultCount int
			rerr        error
		)
		defer func() {
			span.SetAttributes(attribute.Int("results.count", resultCount))
			if rerr != nil {
				span.RecordError(rerr)
				span.SetStatus(codes.Error, rerr.Error())
			}
			span.End()
			if b.metrics != nil {
				b.metrics.recordSearch(ctx, time.Since(start).Seconds(), resultCount, rerr, true)
			}
		}()

		results, err := b.retrieveSearch(ctx, query, inlineResultLimit)
		if err != nil {
			rerr = err
			lg.Error("inline search retrieve", zap.Error(err))
			_, err := sender.Inline(u).Set(ctx)
			return err
		}
		resultCount = len(results)

		ib := sender.Inline(u)
		ib.CacheTimeSeconds(300).Private(true)
		s := newInlineSender(ib)
		_, err = s.setInline(ctx, searchInlineResults(results)...)
		if err != nil {
			if isStaleInlineQueryError(err) {
				lg.Debug("inline search query expired", zap.Error(err))
				return nil
			}
			rerr = err
			lg.Error("inline search set results", zap.Error(err))
		}
		return err
	})

	return client.Run(ctx, func(ctx context.Context) error {
		if _, err := client.Auth().Bot(ctx, b.cred.BotToken); err != nil {
			return errors.Wrap(err, "bot auth")
		}
		if err := b.commands.registerCommands(ctx, raw); err != nil {
			b.logger.Warn("register bot commands failed", zap.Error(err))
		}
		b.logger.Info("bot authenticated, serving /context, /search, /investigate, /start, /help")
		<-ctx.Done()
		return ctx.Err()
	})
}

func isStaleInlineQueryError(err error) bool {
	return tgerr.Is(err, "QUERY_ID_INVALID")
}

func (b *Bot) sendTextReply(ctx context.Context, s messageSender, answer string) {
	if _, err := s.sendText(ctx, answer); err != nil {
		b.logger.Error("reply failed", zap.Error(err))
	}
}

func (b *Bot) handleWithProgress(ctx context.Context, s messageSender, query string, handler func(context.Context, string) (index.Answer, error), kind string) {
	lg := zctx.From(ctx)
	msgID := b.sendPlaceholder(ctx, lg, s)
	answer, err := handler(ctx, query)
	if err != nil {
		lg.Error("handle "+kind, zap.Error(err))
		answer = index.Answer{Text: contextFailureMessage(err)}
	}
	if answer.Debug != nil {
		answer.Text = strings.TrimSpace(answer.Text) + "\n\n" + debugMarkdown(answer.Debug)
	}
	lg.Info("replying", zap.String("answer", answer.Text), zap.Int("buttons", len(answer.Links)))
	b.sendOrEditAnswer(ctx, s, answer, msgID, lg, kind)
}

func (b *Bot) sendPlaceholder(ctx context.Context, lg *zap.Logger, s messageSender) int {
	if b.silent {
		return 0
	}
	msgID, err := s.sendText(ctx, "🔍 Searching\u2026")
	if err != nil {
		lg.Warn("failed to send placeholder", zap.Error(err))
	}
	return msgID
}

func (b *Bot) sendOrEditAnswer(ctx context.Context, s messageSender, answer index.Answer, msgID int, lg *zap.Logger, kind string) {
	if b.silent {
		return
	}
	chunks := splitMarkdown(answer.Text, telegramMessageLimit)
	if len(chunks) == 0 {
		chunks = []string{answer.Text}
	}
	kb := linksMarkup(answer.Links)

	editOK := false
	if msgID > 0 {
		// Single-chunk answer: edit carries the buttons (the loop below won't
		// run, so this is the only place to attach them). Multi-chunk: edit
		// the first chunk plain; the loop sends the rest with buttons on last.
		var editKB tg.ReplyMarkupClass
		if len(chunks) == 1 {
			editKB = kb
		}
		if err := s.editStyled(ctx, msgID, chunks[0], editKB); err == nil {
			editOK = true
			chunks = chunks[1:]
		} else if tg.IsMessageNotModified(err) {
			return
		} else {
			lg.Warn(kind+" edit failed, falling back to fresh replies", zap.Error(err))
		}
	}

	if !editOK && msgID > 0 {
		answer.Text = "\u21aa " + answer.Text
		chunks = splitMarkdown(answer.Text, telegramMessageLimit)
		if len(chunks) == 0 {
			chunks = []string{answer.Text}
		}
	}

	for i, chunk := range chunks {
		var chunkKB tg.ReplyMarkupClass
		if kb != nil && i == len(chunks)-1 {
			chunkKB = kb
		}
		if err := s.sendStyled(ctx, chunk, chunkKB); err != nil {
			lg.Error(kind+" send failed", zap.Error(err), zap.Int("chunk", i))
			return
		}
	}
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
	if b.answerer == nil {
		rerr = errors.New("bot answerer is not configured")
		return index.Answer{}, rerr
	}

	q := index.Query{Text: query, Limit: 12}
	var results []index.Result
	if b.retriever != nil {
		var err error
		results, err = b.retriever.Retrieve(ctx, q)
		if err != nil {
			rerr = errors.Wrap(err, "retrieve")
			return index.Answer{}, rerr
		}
		resultCount = len(results)
	}
	answer, err := b.answerer.Answer(ctx, q, results)
	if err != nil {
		rerr = errors.Wrap(err, "answer")
		return index.Answer{}, rerr
	}
	return answer, nil
}

// handleSearch runs raw retrieval (no LLM/answerer) and formats results for
// the /search command.
func (b *Bot) handleSearch(ctx context.Context, query string) (index.Answer, error) {
	start := time.Now()
	ctx, span := b.tracer.Start(ctx, "bot.search",
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
			b.metrics.recordSearch(ctx, time.Since(start).Seconds(), resultCount, rerr, false)
		}
		span.SetAttributes(attribute.Int("results.count", resultCount))
		if rerr != nil {
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.End()
	}()

	results, err := b.retrieveSearch(ctx, query, searchResultLimit)
	if err != nil {
		rerr = errors.Wrap(err, "retrieve")
		return index.Answer{}, rerr
	}
	resultCount = len(results)

	return index.Answer{
		Text: searchResultsText(results),
	}, nil
}

// investigateAsync runs an investigation in the background and delivers the
// report as one or more follow-up replies, so the caller (the OnNewMessage
// dispatch loop) never blocks on it.
func (b *Bot) investigateAsync(ctx context.Context, s messageSender, description string) {
	report, err := b.handleInvestigate(ctx, description)
	if err != nil {
		b.logger.Error("handle investigate", zap.Error(err))
		if !b.silent {
			b.sendTextReply(ctx, s, investigateFailureMessage(err))
		}
		return
	}
	b.logger.Info("investigate reply", zap.String("verdict", string(report.Verdict)))
	b.sendAnswer(ctx, s, index.Answer{Text: reportMarkdown(report), Links: report.Links}, b.logger, "investigate")
}

// sendAnswer delivers answer as one or more replies, splitting the Markdown
// text on paragraph boundaries so no single message exceeds
// telegramMessageLimit (Telegram rejects/mangles oversized messages
// otherwise). Link buttons are attached to the final chunk only, so they sit
// at the bottom of the whole reply. kind labels log lines (e.g. "context",
// "search", "investigate").
func (b *Bot) sendAnswer(ctx context.Context, s messageSender, answer index.Answer, lg *zap.Logger, kind string) {
	if b.silent {
		return
	}
	chunks := splitMarkdown(answer.Text, telegramMessageLimit)
	if len(chunks) == 0 {
		chunks = []string{answer.Text}
	}
	kb := linksMarkup(answer.Links)
	for i, chunk := range chunks {
		var chunkKB tg.ReplyMarkupClass
		if kb != nil && i == len(chunks)-1 {
			chunkKB = kb
		}
		if err := s.sendStyled(ctx, chunk, chunkKB); err != nil {
			lg.Error(kind+" send failed", zap.Error(err), zap.Int("chunk", i))
			return
		}
	}
}

// contextFailureMessage picks a user-facing message for a failed /context
// (or /search) request, distinguishing a timeout from other failures instead
// of one generic "something went wrong" for every cause.
func contextFailureMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Sorry, that took too long to answer. Try a narrower question."
	}
	return "Sorry, something went wrong handling that request."
}

// investigateTraceIDPattern matches the trace_id cmd/ssagent's runJob embeds
// in a failed job's error message (see its "trace_id=" wrap) — the OTel
// trace ID itself doesn't survive the ssagent -> ssbot HTTP/JSON boundary any
// other way, only the rendered error string does.
var investigateTraceIDPattern = regexp.MustCompile(`trace_id=([0-9a-f]{32})`)

// investigateFailureMessage picks a user-facing message for a failed
// /investigate request, distinguishing a timeout and iteration exhaustion
// from other failures instead of one generic "investigation failed" for
// every cause. Appends the trace ID when the error carries one, so a failure
// can still be looked up.
func investigateFailureMessage(err error) string {
	msg := "Sorry, investigation failed."
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		msg = "Sorry, the investigation timed out before reaching a conclusion."
	case errors.Is(err, agent.ErrMaxIterations):
		msg = "Sorry, the investigation used too many steps without reaching a conclusion. Try narrowing the question."
	}
	if m := investigateTraceIDPattern.FindStringSubmatch(err.Error()); m != nil {
		msg += fmt.Sprintf("\ntrace_id: %s", m[1])
	}
	return msg
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
