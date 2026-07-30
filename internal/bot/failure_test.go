package bot

import (
	"context"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
)

// An error from ssapi carries internals — DSNs, hostnames, constraint names,
// other users' identities — so the chat gets a generic line plus a
// correlation id, and the detail goes to the log.
func TestReplyFailureHidesTheError(t *testing.T) {
	b := newNotifyTestBot(newFakeNotifier())
	stub, sent := captureSend(t)

	err := errors.New(`decode response: code 500: pq: duplicate key "users_gitlab_username_key" on host db-internal-1`)
	b.replyFailure(context.Background(), stub, "subscribing", err)

	require.Contains(t, *sent, "subscribing failed")
	require.Contains(t, *sent, "trace_id: ")
	require.NotContains(t, *sent, "db-internal-1")
	require.NotContains(t, *sent, "users_gitlab_username_key")
}

// Every failure reply must carry an id, including the untraced case: the id is
// the only thing tying a user's report to a log line.
func TestFailureRefAlwaysPresent(t *testing.T) {
	require.NotEmpty(t, failureRef(context.Background()))
}

func TestHandleSubscribeCmdFailureIsGeneric(t *testing.T) {
	n := newFakeNotifier()
	n.err = errors.New("notify subscribe: code 503: {ErrorMessage:notification system not configured}")
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("subscribe")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: "gitlab"}))
	require.NotContains(t, *sent, "503")
	require.Contains(t, *sent, "trace_id: ")
}
