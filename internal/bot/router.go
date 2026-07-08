package bot

import (
	"strings"
)

// parseCommand extracts the command name and the rest of the message.
// It tolerates a bot mention suffix, e.g. "/context@sisyphus foo".
func parseCommand(text string) (cmd, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}

	cmdPart := text
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		cmdPart = text[:i]
		rest = strings.TrimSpace(text[i:])
	}

	// Drop an optional @botname token.
	if i := strings.Index(cmdPart, "@"); i >= 0 {
		cmdPart = cmdPart[:i]
	}

	cmd = strings.TrimPrefix(cmdPart, "/")

	if cmd == "" {
		return "", "", false
	}
	return cmd, rest, true
}
