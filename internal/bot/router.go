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

// parseInlineQuery strips an optional "search " or "/search " prefix from an
// inline query string. Returns the remaining text.
func parseInlineQuery(text string) string {
	text = strings.TrimSpace(text)
	// Strip optional leading "search " or "/search ".
	for _, prefix := range []string{"/search ", "search "} {
		if strings.HasPrefix(strings.ToLower(text), prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	return text
}
