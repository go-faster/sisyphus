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
	b.replyFailure(context.Background(), stub, "subscribe", err)

	require.Contains(t, *sent, "/subscribe failed")
	require.Contains(t, *sent, "trace_id: ")
	require.NotContains(t, *sent, "db-internal-1")
	require.NotContains(t, *sent, "users_gitlab_username_key")
}

// Every failure reply must carry an id, including the untraced case: the id is
// the only thing tying a user's report to a log line.
func TestFailureRefAlwaysPresent(t *testing.T) {
	require.NotEmpty(t, failureRef(context.Background()))
}

// The point of routing failures through dispatch: a handler returns the error
// and the generic reply happens once, for every command, without the handler
// taking part.
func TestDispatchTurnsAHandlerErrorIntoAGenericReply(t *testing.T) {
	n := newFakeNotifier()
	n.err = errors.New("notify subscribe: code 503: {ErrorMessage:notification system not configured}")
	b := newNotifyTestBot(n)
	stub, sent := captureSend(t)

	b.dispatch(context.Background(), stub, "subscribe", "gitlab", invocation{SenderID: 42, Rest: "gitlab"})

	require.Contains(t, *sent, "/subscribe failed")
	require.NotContains(t, *sent, "503")
	require.NotContains(t, *sent, "not configured")
}

func TestDispatchRepliesUsageAndHelp(t *testing.T) {
	b := newNotifyTestBot(newFakeNotifier())

	stub, sent := captureSend(t)
	b.dispatch(context.Background(), stub, "subscribe", "", invocation{SenderID: 42})
	require.Contains(t, *sent, "Usage: /subscribe")

	// An unknown command answers in a private chat...
	stub, sent = captureSend(t)
	b.dispatch(context.Background(), stub, "nope", "", invocation{SenderID: 42, Chat: chatPeer{Type: peerTypeUser, ID: 42}})
	require.Contains(t, *sent, "Unknown command /nope")

	// ...and stays quiet in a group, where it was likely meant for another bot.
	stub, sent = captureSend(t)
	b.dispatch(context.Background(), stub, "nope", "", invocation{SenderID: 42, Chat: chatPeer{Type: peerTypeChannel, ID: -100}})
	require.Empty(t, *sent)
}
