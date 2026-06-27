// Package bot implements the Telegram bot that serves the /context command
// over MTProto via gotd (plan §10, §14).
package bot

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/log/logzap"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
)

// Retriever is the retrieval subset the bot needs.
type Retriever interface {
	Retrieve(ctx context.Context, q index.Query) ([]index.Result, error)
}

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
	log       *zap.Logger
}

// New builds a Bot.
func New(cfg Config, r Retriever, a index.Answerer, log *zap.Logger) *Bot {
	if log == nil {
		log = zap.NewNop()
	}
	return &Bot{cfg: cfg, retriever: r, answerer: a, log: log}
}

// Run connects, authenticates as a bot, and serves updates until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	dispatcher := tg.NewUpdateDispatcher()
	client := telegram.NewClient(b.cfg.AppID, b.cfg.AppHash, telegram.Options{
		Logger:         logzap.New(b.log.Named("td")),
		UpdateHandler:  dispatcher,
		SessionStorage: &telegram.FileSessionStorage{Path: filepath.Join(b.cfg.SessionDir, "bot.json")},
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
		b.log.Info("context command", zap.String("query", query))

		answer, err := b.handle(ctx, query)
		if err != nil {
			b.log.Error("handle context", zap.Error(err))
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
		b.log.Info("bot authenticated, serving /context")
		<-ctx.Done()
		return ctx.Err()
	})
}

func (b *Bot) handle(ctx context.Context, query string) (string, error) {
	results, err := b.retriever.Retrieve(ctx, index.Query{Text: query, Limit: 12})
	if err != nil {
		return "", errors.Wrap(err, "retrieve")
	}
	answer, err := b.answerer.Answer(ctx, query, results)
	if err != nil {
		return "", errors.Wrap(err, "answer")
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
