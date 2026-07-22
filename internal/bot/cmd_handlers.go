package bot

import (
	"context"
	"fmt"

	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// buildCommandRegistry is the single source of truth for the bot's commands:
// dispatch (OnNewMessage lookup), /help and /start text (helpText), and
// Telegram's native /-autocomplete menu (registerCommands -> BotsSetBotCommands).
// runCtx is the process-lifetime context used for /investigate's offloaded
// goroutine, which must outlive the per-update callback that spawned it.
func (b *Bot) buildCommandRegistry(runCtx context.Context) *commandRegistry {
	reg := newCommandRegistry()
	reg.add("start", "", "Show your user ID and available commands", true, b.handleStartCmd)
	reg.add("help", "", "Show this message", false, b.handleHelpCmd)
	reg.add("context", "<question>", "search indexed knowledge and answer a question", false, b.handleContextCmd)
	reg.add("search", "<query>", "raw ranked search results, no summary", false, b.handleSearchCmd)
	reg.add("investigate", "<description>", "run an on-demand investigation", false,
		func(ctx context.Context, s messageSender, _ int64, rest string) error {
			return b.handleInvestigateCmd(runCtx, ctx, s, rest)
		},
	)
	reg.add("link", "<gitlab|jira> <identity> [display name]", "link your GitLab/Jira identity for notifications", false, b.handleLinkCmd)
	reg.add("subscribe", "<gitlab|jira> [event_type ...]", "subscribe to GitLab/Jira notifications", false, b.handleSubscribeCmd)
	reg.add("unsubscribe", "<gitlab|jira>", "unsubscribe from notifications", false, b.handleUnsubscribeCmd)
	reg.add("notifications", "", "list your notification subscriptions", false, b.handleNotificationsCmd)
	return reg
}

// handleStartCmd replies with the user's ID and the generated help text.
func (b *Bot) handleStartCmd(ctx context.Context, s messageSender, senderID int64, _ string) error {
	zctx.From(ctx).Info("start command", zap.Int64("user_id", senderID))
	if b.silent {
		return nil
	}
	b.sendTextReply(ctx, s, fmt.Sprintf("Your ID: %d\n\n%s", senderID, b.commands.helpText()))
	return nil
}

// handleHelpCmd replies with the generated help text.
func (b *Bot) handleHelpCmd(ctx context.Context, s messageSender, _ int64, _ string) error {
	zctx.From(ctx).Info("help command")
	if b.silent {
		return nil
	}
	b.sendTextReply(ctx, s, b.commands.helpText())
	return nil
}

// handleContextCmd runs /context with the send-then-edit progress flow.
func (b *Bot) handleContextCmd(ctx context.Context, s messageSender, _ int64, rest string) error {
	zctx.From(ctx).Info("context command", zap.String("query", rest))
	b.handleWithProgress(ctx, s, rest, b.handle, "context")
	return nil
}

// handleSearchCmd runs /search with the send-then-edit progress flow.
func (b *Bot) handleSearchCmd(ctx context.Context, s messageSender, _ int64, rest string) error {
	zctx.From(ctx).Info("search command", zap.String("query", rest))
	b.handleWithProgress(ctx, s, rest, b.handleSearch, "search")
	return nil
}

// handleInvestigateCmd acks immediately and offloads /investigate to a
// background goroutine rooted in runCtx, so the OnNewMessage dispatch loop
// never blocks on the (minutes-long) investigation.
func (b *Bot) handleInvestigateCmd(runCtx, ctx context.Context, s messageSender, rest string) error {
	zctx.From(ctx).Info("investigate command", zap.String("description", rest))
	if b.investigator == nil {
		if !b.silent {
			b.sendTextReply(ctx, s, "Investigation capability is not configured.")
		}
		return nil
	}
	if !b.silent {
		b.sendTextReply(ctx, s, "Investigating, this may take a few minutes. I'll follow up here.")
	}
	go b.investigateAsync(runCtx, s, rest)
	return nil
}
