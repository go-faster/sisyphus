package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/gotd/td/tg"
)

type fakeNotifier struct {
	registered                   []NotifyChat
	registerErr                  error
	lastHash                     int64
	lastAddedBy                  int64
	enrolledUserID, enrolledHash int64
	subscribed                   map[int64][2]string // telegramUserID -> [source, joined event types]
	unsubscribed                 map[int64]string
	subs                         []NotifySubscription
	err                          error
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{
		subscribed:   map[int64][2]string{},
		unsubscribed: map[int64]string{},
	}
}

func (f *fakeNotifier) NotifyEnroll(_ context.Context, telegramUserID, accessHash int64) error {
	f.enrolledUserID, f.enrolledHash = telegramUserID, accessHash
	return f.err
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

func (f *fakeNotifier) NotifyRegisterChat(_ context.Context, peerType string, peerID, accessHash int64, title string, addedBy int64, enabled bool) error {
	if f.registerErr != nil {
		return f.registerErr
	}
	f.registered = append(f.registered, NotifyChat{PeerType: peerType, PeerID: peerID, Title: title, Enabled: enabled})
	f.lastHash = accessHash
	f.lastAddedBy = addedBy
	return nil
}

func (f *fakeNotifier) NotifyListChats(context.Context) ([]NotifyChat, error) {
	return f.registered, nil
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
	err := b.SendTo(context.Background(), uuid.New(), "user", 1, 2, "hello")
	require.ErrorIs(t, err, errBotNotReady)
}

func TestNotifyPeer(t *testing.T) {
	require.Equal(t, &tg.InputPeerUser{UserID: 7, AccessHash: 9}, notifyPeer("user", 7, 9))
	require.Equal(t, &tg.InputPeerUser{UserID: 7, AccessHash: 9}, notifyPeer("", 7, 9),
		"an unset peer type is a user, as every row written before broadcasts existed is")
	require.Equal(t, &tg.InputPeerChannel{ChannelID: 7, AccessHash: 9}, notifyPeer("channel", 7, 9))
	require.Equal(t, &tg.InputPeerChat{ChatID: 7}, notifyPeer("chat", 7, 9))
}

func TestRandomIDFor_DeterministicPerNotification(t *testing.T) {
	id1, id2 := uuid.New(), uuid.New()
	require.Equal(t, randomIDFor(id1), randomIDFor(id1), "same notification must reuse the same random_id on retry")
	require.NotEqual(t, randomIDFor(id1), randomIDFor(id2))
}

// /link is gone: identity is decided in deployment config (notify.identities),
// so the command must not be registered at all.
func TestLinkCommandRemoved(t *testing.T) {
	b := newNotifyTestBot(newFakeNotifier())
	_, ok := b.commands.lookup("link")
	require.False(t, ok)
}

func TestHandleSubscribeCmd_DefaultsEventTypes(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("subscribe")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: "gitlab"}))
	require.Equal(t, [2]string{"gitlab", "mr_assigned,mr_review_requested"}, n.subscribed[42])
	require.Contains(t, *sent, "Subscribed to gitlab")
}

func TestHandleSubscribeCmd_ExplicitEventTypes(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("subscribe")
	stub, _ := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: "jira issue_assigned"}))
	require.Equal(t, [2]string{"jira", "issue_assigned"}, n.subscribed[42])
}

func TestHandleUnsubscribeCmd(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("unsubscribe")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: "gitlab"}))
	require.Equal(t, "gitlab", n.unsubscribed[42])
	require.Contains(t, *sent, "Unsubscribed from gitlab")
}

func TestHandleNotificationsCmd_ListsSubscriptions(t *testing.T) {
	n := newFakeNotifier()
	n.subs = []NotifySubscription{{Source: "gitlab", EventTypes: []string{"mr_assigned"}, Enabled: true}}
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("notifications")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: ""}))
	require.Contains(t, *sent, "gitlab (enabled): mr_assigned")
}

func TestHandleNotificationsCmd_Empty(t *testing.T) {
	n := newFakeNotifier()
	b := newNotifyTestBot(n)
	c, _ := b.commands.lookup("notifications")
	stub, sent := captureSend(t)

	require.NoError(t, c.handler(context.Background(), stub, invocation{SenderID: 42, Rest: ""}))
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
