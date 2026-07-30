package bot

import (
	"context"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/tg"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// invocation is one command call: who sent it, where they sent it, and the
// argument text. The chat matters because a command may act on the chat it
// was sent in rather than on its sender — /alerts registers the current
// channel, and the peer it needs (access hash included) exists only in the
// update that carried the command.
type invocation struct {
	SenderID int64
	Chat     chatPeer
	Rest     string
}

// Peer kinds a command can arrive from. They match notify.PeerType's values,
// which is what the notification store persists.
const (
	peerTypeUser    = "user"
	peerTypeChat    = "chat"    // basic group
	peerTypeChannel = "channel" // channel or supergroup
)

// chatPeer is the MTProto peer a command arrived from, flattened to what the
// notification store persists.
type chatPeer struct {
	Type       string
	ID         int64
	AccessHash int64 // zero for a basic group, which needs none
	Title      string
}

// commandHandler serves a single command invocation.
type commandHandler func(ctx context.Context, s messageSender, inv invocation) error

// command is a registered Telegram bot command.
type command struct {
	name    string // "context" (no leading slash)
	usage   string // "<question>", shown in /help; empty for no-arg commands
	desc    string // shown in /help and Telegram's /-menu; empty hides it
	hidden  bool   // hidden commands are omitted from /help and the /-menu
	handler commandHandler
}

// dispatch runs one command: it resolves the name, enforces the usage
// contract, and — the reason it exists as one function — turns whatever the
// handler returns into the single reply path for failures.
//
// A handler reports a failure by returning the error, not by writing to the
// chat. Doing it per-handler is how raw ssapi errors (DSNs, hostnames,
// constraint names, other users' identities) ended up in messages, and every
// new command would have to remember the rule. Here it cannot be forgotten.
//
// /context, /search and /investigate still answer their own failures: they
// edit a progress placeholder and distinguish a timeout or an exhausted
// iteration budget from a plain failure, which a generic reply cannot. They
// return nil, so this path stays out of their way.
func (b *Bot) dispatch(ctx context.Context, s messageSender, name, rest string, inv invocation) {
	c, ok := b.commands.lookup(name)
	if !ok {
		// Answer with the command list in a private chat, but stay quiet in a
		// group or channel, where a slash command is just as likely addressed
		// to some other bot.
		if inv.Chat.Type == peerTypeUser {
			b.sendTextReply(ctx, s, "Unknown command /"+name+".\n\n"+b.commands.helpText())
		}
		return
	}
	// A command whose usage is non-empty needs arguments. Answering with that
	// usage is the whole point of recording it — a bare /link used to do
	// nothing at all, which reads as a broken bot.
	if rest == "" && c.usage != "" {
		b.sendTextReply(ctx, s, "Usage: /"+c.name+" "+c.usage)
		return
	}

	// One span per command: it is what makes the trace_id in a failure reply
	// resolve to something an operator can open.
	ctx, span := b.tracer.Start(ctx, "bot.command",
		trace.WithAttributes(attribute.String("command", c.name)))
	defer span.End()

	if err := c.handler(ctx, s, inv); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		b.replyFailure(ctx, s, c.name, err)
	}
}

// commandRegistry is the Bot's single source of truth for command dispatch,
// /help text, and Telegram's native /-menu registration.
type commandRegistry struct {
	cmds   []command
	byName map[string]int
}

func newCommandRegistry() *commandRegistry {
	return &commandRegistry{byName: map[string]int{}}
}

// add registers a command. Duplicate names overwrite the previous entry.
func (r *commandRegistry) add(name, usage, desc string, hidden bool, h commandHandler) {
	if h == nil {
		panic("commandRegistry: nil handler for " + name)
	}
	if existing, ok := r.byName[name]; ok {
		r.cmds[existing] = command{name, usage, desc, hidden, h}
		return
	}
	r.byName[name] = len(r.cmds)
	r.cmds = append(r.cmds, command{name, usage, desc, hidden, h})
}

// lookup returns the command registered under name.
func (r *commandRegistry) lookup(name string) (command, bool) {
	i, ok := r.byName[name]
	if !ok {
		return command{}, false
	}
	return r.cmds[i], true
}

// helpText builds the /help, /start response from the registered commands, in
// registration order. Commands that are hidden or have an empty description
// are omitted.
func (r *commandRegistry) helpText() string {
	var sb strings.Builder
	sb.WriteString("Available commands:")
	for _, c := range r.cmds {
		if c.hidden || c.desc == "" {
			continue
		}
		sb.WriteString("\n/")
		sb.WriteString(c.name)
		if c.usage != "" {
			sb.WriteString(" ")
			sb.WriteString(c.usage)
		}
		sb.WriteString(" \u2014 ")
		sb.WriteString(c.desc)
	}
	return sb.String()
}

// botCommands returns the non-hidden, described commands in registration
// order, ready for BotsSetBotCommands.
func (r *commandRegistry) botCommands() []tg.BotCommand {
	out := make([]tg.BotCommand, 0, len(r.cmds))
	for _, c := range r.cmds {
		if c.hidden || c.desc == "" {
			continue
		}
		out = append(out, tg.BotCommand{
			Command:     c.name,
			Description: c.desc,
		})
	}
	return out
}

// registerCommands publishes the non-hidden, described commands to Telegram's
// native command picker (the "/" autocomplete menu) via BotsSetBotCommands.
// Safe to call once after the bot authenticates; idempotent.
func (r *commandRegistry) registerCommands(ctx context.Context, raw *tg.Client) error {
	cmds := r.botCommands()
	if len(cmds) == 0 {
		return nil
	}
	if _, err := raw.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
		Scope:    &tg.BotCommandScopeDefault{},
		LangCode: "en",
		Commands: cmds,
	}); err != nil {
		return errors.Wrap(err, "set bot commands")
	}
	return nil
}
