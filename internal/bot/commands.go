package bot

import (
	"context"
	"strings"

	"github.com/go-faster/errors"

	"github.com/gotd/td/tg"
)

// commandHandler serves a single command invocation. senderID is the user who
// sent the message; rest is the command's whitespace-trimmed argument text.
type commandHandler func(ctx context.Context, s messageSender, senderID int64, rest string) error

// command is a registered Telegram bot command.
type command struct {
	name    string // "context" (no leading slash)
	usage   string // "<question>", shown in /help; empty for no-arg commands
	desc    string // shown in /help and Telegram's /-menu; empty hides it
	hidden  bool   // hidden commands are omitted from /help and the /-menu
	handler commandHandler
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
