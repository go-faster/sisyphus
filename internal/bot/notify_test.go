package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/gotd/td/tg"
)

type fakeNotifier struct {
	enrolledUserID, enrolledHash int64
	gitlabLinks                  map[int64]string
	jiraLinks                    map[int64]string
	jiraDisplayNames             map[int64]string
	subscribed                   map[int64][2]string // telegramUserID -> [source, joined event types]
	unsubscribed                 map[int64]string
	subs                         []NotifySubscription
	err                          error
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{
		gitlabLinks:      map[int64]string{},
		jiraLinks:        map[int64]string{},
		jiraDisplayNames: map[int64]string{},
		subscribed:       map[int64][2]string{},
		unsubscribed:     map[int64]string{},
	}
}

func (f *fakeNotifier) NotifyEnroll(_ context.Context, telegramUserID, accessHash int64) error {
	f.enrolledUserID, f.enrolledHash = telegramUserID, accessHash
	return f.err
}

func (f *fakeNotifier) NotifyLinkGitLab(_ context.Context, telegramUserID int64, username string) error {
	if f.err != nil {
		return f.err
	}
	f.gitlabLinks[telegramUserID] = username
	return nil
}

func (f *fakeNotifier) NotifyLinkJira(_ context.Context, telegramUserID int64, accountID, displayName string) error {
	if f.err != nil {
		return f.err
	}
	f.jiraLinks[telegramUserID] = accountID
	f.jiraDisplayNames[telegramUserID] = displayName
	return nil
}

func (f *fakeNotifier) NotifySubscribe(_ context.Context, telegramUserID int64, source string, eventTypes []string) error {
	if f.err != nil {
		return f.err
	}
	var joined strings.Builder
	for i, t := range eventTypes {
		if i > 0 {
			joined.WriteString(",")
		}
		joined.WriteString(t)
	}
	f.subscribed[telegramUserID] = [2]string{source, joined.String()}
	return nil
}

func (f *fakeNotifier) NotifyUnsubscribe(_ context.Context, telegramUserID int64, source string) error {
	if f.err != nil {
		return f.err
	}
	f.unsubscribed[telegramUserID] = source
	return nil
}

func (f *fakeNotifier) NotifyListSubscriptions(_ context.Context, _ int64) ([]NotifySubscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.subs, nil
}

func newNotifyTestBot(notifier Notifier) *Bot {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         false,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
		Notifier:       notifier,
	})
	b.commands = b.buildCommandRegistry(context.Background())
	return b
}

func captureSend(t *testing.T) (stub *fakeSender, sent *string) {
	t.Helper()
	sent = new(string)
	stub = &fakeSender{onSendText: func(_ context.Context, text string) (int, error) {
		*sent = text
		return 0, nil
	}}
	return stub, sent
}

func TestSendTo_NotReadyBeforeRun(t *testing.T) {
	b := newNotifyTestBot(nil)
	err := b.SendTo(context.Background(), 1, 2, "hello")
	require.ErrorIs(t, err, errBotNotReady)
}

func TestHandleLinkCmd_GitLab(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("link")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "gitlab alice"))
	require.Equal(t, "alice", n.gitlabLinks[42])
	require.Contains(t, *sent, "Linked gitlab identity: alice")
}

func TestHandleLinkCmd_Jira(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("link")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "jira acc-1 Alice A"))
	require.Equal(t, "acc-1", n.jiraLinks[42])
	require.Equal(t, "Alice A", n.jiraDisplayNames[42])
	require.Contains(t, *sent, "Linked jira identity: acc-1")
}

func TestHandleLinkCmd_UnknownSource(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("link")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "slack alice"))
	require.Contains(t, *sent, "Unknown source")
	require.Empty(t, n.gitlabLinks)
}

func TestHandleLinkCmd_NoNotifierConfigured(t *testing.T) {
	b := newNotifyTestBot(nil)
	c, _ := b.commands.lookup("link")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "gitlab alice"))
	require.Contains(t, *sent, "not configured")
}

func TestHandleSubscribeCmd_DefaultsEventTypes(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("subscribe")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "gitlab"))
	require.Equal(t, [2]string{"gitlab", "mr_assigned,mr_review_requested"}, n.subscribed[42])
	require.Contains(t, *sent, "Subscribed to gitlab")
}

func TestHandleSubscribeCmd_ExplicitEventTypes(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("subscribe")
	stub, _ := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "jira issue_assigned"))
	require.Equal(t, [2]string{"jira", "issue_assigned"}, n.subscribed[42])
}

func TestHandleUnsubscribeCmd(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("unsubscribe")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, "gitlab"))
	require.Equal(t, "gitlab", n.unsubscribed[42])
	require.Contains(t, *sent, "Unsubscribed from gitlab")
}

func TestHandleNotificationsCmd_ListsSubscriptions(t *testing.T) {
	n := newFakeNotifier()
	n.subs = []NotifySubscription{{Source: "gitlab", EventTypes: []string{"mr_assigned"}, Enabled: true}}
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("notifications")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, ""))
	require.Contains(t, *sent, "gitlab (enabled): mr_assigned")
}

func TestHandleNotificationsCmd_Empty(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("notifications")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, 42, ""))
	require.Contains(t, *sent, "No subscriptions")
}

func TestCaptureNotifyIdentity_EnrollsFromEntities(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)

	entities := tg.Entities{Users: map[int64]*tg.User{42: {ID: 42, AccessHash: 999}}}
	b.captureNotifyIdentity(context.Background(), entities, 42)

	require.EqualValues(t, 42, n.enrolledUserID)
	require.EqualValues(t, 999, n.enrolledHash)
}

func TestCaptureNotifyIdentity_NoNotifierIsNoOp(t *testing.T) {
	b := newNotifyTestBot(nil)
	entities := tg.Entities{Users: map[int64]*tg.User{42: {ID: 42, AccessHash: 999}}}
	// Must not panic with a nil notifier.
	b.captureNotifyIdentity(context.Background(), entities, 42)
}

func TestCaptureNotifyIdentity_UnknownSenderIsNoOp(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	entities := tg.Entities{Users: map[int64]*tg.User{}}
	b.captureNotifyIdentity(context.Background(), entities, 42)
	require.Zero(t, n.enrolledUserID)
}
