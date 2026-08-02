package bot

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// sendAttempt is one messages.sendMessage the bot issued.
type sendAttempt struct {
	randomID int64
	markup   tg.ReplyMarkupClass
	message  string
}

// recordingInvoker stands in for Telegram: it records every sendMessage and
// answers with whatever reply the test's rule returns.
type recordingInvoker struct {
	attempts []sendAttempt
	// reply decides an attempt's fate from whether it carried a keyboard.
	reply func(hasMarkup bool) error
}

func (r *recordingInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.MessagesSendMessageRequest)
	if !ok {
		return tgerr.New(400, "UNEXPECTED_REQUEST")
	}
	markup, _ := req.GetReplyMarkup()
	r.attempts = append(r.attempts, sendAttempt{randomID: req.RandomID, markup: markup, message: req.Message})
	if err := r.reply(markup != nil); err != nil {
		return err
	}
	if box, ok := output.(*tg.UpdatesBox); ok {
		box.Updates = &tg.Updates{}
	}
	return nil
}

func sendingBot(t *testing.T, inv *recordingInvoker) *Bot {
	t.Helper()
	b := &Bot{}
	sender := message.NewSender(tg.NewClient(inv))
	b.sender.Store(sender)
	return b
}

var testButtons = []index.Link{{Text: "Dashboard", URL: "https://grafana.example.com/d/1"}}

// The message matters more than its buttons: a keyboard Telegram refuses is
// dropped and the text goes out anyway. This is the bug that lost a batch of
// alert notifications outright.
func TestSendTo_DropsKeyboardWhenTelegramRefusesIt(t *testing.T) {
	inv := &recordingInvoker{reply: func(hasMarkup bool) error {
		if hasMarkup {
			return tgerr.New(400, "BUTTON_URL_INVALID")
		}
		return nil
	}}

	err := sendingBot(t, inv).SendTo(context.Background(), uuid.New(), "user", 7, 9, "prod is on fire", testButtons)
	require.NoError(t, err)

	require.NotEmpty(t, inv.attempts)
	last := inv.attempts[len(inv.attempts)-1]
	require.Nil(t, last.markup, "the surviving attempt carries no keyboard")
	require.Contains(t, last.message, "prod is on fire", "and still carries the whole message")

	// Telegram dedups on random_id, so a first attempt that in fact landed but
	// whose response was lost returns that message rather than posting twice.
	for _, a := range inv.attempts {
		require.Equal(t, inv.attempts[0].randomID, a.randomID)
	}
}

// A failure that has nothing to do with the keyboard is still a failure: the
// fallback must not turn an undeliverable message into a silent success.
func TestSendTo_ReturnsErrorWhenEveryAttemptFails(t *testing.T) {
	inv := &recordingInvoker{reply: func(bool) error { return tgerr.New(403, "USER_IS_BLOCKED") }}

	err := sendingBot(t, inv).SendTo(context.Background(), uuid.New(), "user", 7, 9, "hello", testButtons)
	require.Error(t, err)
}

// With no keyboard to drop there is nothing to degrade to, so a failure
// returns immediately rather than retrying the same request again.
func TestSendTo_NoKeyboardDoesNotRetryTheSameRequest(t *testing.T) {
	inv := &recordingInvoker{reply: func(bool) error { return tgerr.New(400, "MESSAGE_EMPTY") }}

	err := sendingBot(t, inv).SendTo(context.Background(), uuid.New(), "user", 7, 9, "hello", nil)
	require.Error(t, err)
	// Styled, then plain: the pre-existing markdown fallback, and no more.
	require.Len(t, inv.attempts, 2)
	for _, a := range inv.attempts {
		require.Nil(t, a.markup)
	}
}

// A button that could never resolve on the recipient's device is dropped
// before the send, so the happy path stays one request.
func TestSendTo_UnreachableButtonNeverReachesTelegram(t *testing.T) {
	inv := &recordingInvoker{reply: func(bool) error { return nil }}

	err := sendingBot(t, inv).SendTo(context.Background(), uuid.New(), "user", 7, 9, "prod is on fire",
		[]index.Link{{Text: "Alertmanager", URL: "http://a9869748c05a:9093"}})
	require.NoError(t, err)
	require.Len(t, inv.attempts, 1)
	require.Nil(t, inv.attempts[0].markup)
}
