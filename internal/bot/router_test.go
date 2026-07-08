package bot

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		text     string
		wantCmd  string
		wantRest string
		wantOk   bool
	}{
		{"/context foo", "context", "foo", true},
		{"/context@sisyphus foo", "context", "foo", true},
		{"/context\tfoo", "context", "foo", true},
		{"/context\nfoo", "context", "foo", true},
		{"/investigate something bad", "investigate", "something bad", true},
		{"/investigate@bot something bad", "investigate", "something bad", true},
		{"/help", "help", "", true},
		{"not a command", "", "", false},
		{" /context  bar ", "context", "bar", true},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			cmd, rest, ok := parseCommand(tt.text)
			require.Equal(t, tt.wantOk, ok)
			require.Equal(t, tt.wantCmd, cmd)
			require.Equal(t, tt.wantRest, rest)
		})
	}
}
