package bot

import (
	"context"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
	"github.com/go-faster/sisyphus/internal/index"
	"github.com/go-faster/sisyphus/internal/indextest"
)

type captureQueryAnswerer struct {
	query    index.Query
	deadline time.Time
}

func (c *captureQueryAnswerer) Answer(ctx context.Context, q index.Query, _ []index.Result) (index.Answer, error) {
	c.query = q
	c.deadline, _ = ctx.Deadline()
	return index.Answer{Text: "direct answer"}, nil
}

func TestInvestigateFailureMessage_DistinguishesCauses(t *testing.T) {
	require.Contains(t, investigateFailureMessage(context.DeadlineExceeded), "timed out")
	require.Contains(t, investigateFailureMessage(agent.ErrMaxIterations), "too many steps")
	require.Equal(t, "Sorry, investigation failed.", investigateFailureMessage(errors.New("boom")))
	// Errors wrapped along the way (HTTP round trip, ctx propagation) must
	// still classify correctly via errors.Is, not just bare sentinels.
	require.Contains(t, investigateFailureMessage(errors.Wrap(context.DeadlineExceeded, "wait for investigation")), "timed out")
}

func TestContextFailureMessage_DistinguishesTimeout(t *testing.T) {
	require.Contains(t, contextFailureMessage(context.DeadlineExceeded), "took too long")
	require.Equal(t, "Sorry, something went wrong handling that request.", contextFailureMessage(errors.New("boom")))
}

func TestHandleKeepsAnswerLinks(t *testing.T) {
	want := index.Answer{
		Text:  "prod is red because X",
		Links: []index.Link{{Text: "Dashboard", URL: "https://grafana/d/1"}},
	}
	a := &indextest.MockAnswerer{AnswerResult: want}
	b := New(context.Background(), nil, a, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})
	require.NotNil(t, b.answerer)

	got, err := b.handle(context.Background(), "why is prod red?")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestLinksMarkup(t *testing.T) {
	require.Nil(t, linksMarkup(nil))

	kb := linksMarkup([]index.Link{
		{Text: "Dashboard", URL: "https://grafana/d/1"},
		{Text: "Ticket", URL: "https://jira/IDP-1"},
	})
	markup, ok := kb.(*tg.ReplyInlineMarkup)
	require.True(t, ok)
	require.Len(t, markup.Rows, 2)
	btn, ok := markup.Rows[0].Buttons[0].(*tg.KeyboardButtonURL)
	require.True(t, ok)
	require.Equal(t, "Dashboard", btn.Text)
	require.Equal(t, "https://grafana/d/1", btn.URL)
}

func TestPeerChatID(t *testing.T) {
	tests := []struct {
		name     string
		peer     tg.PeerClass
		expected int64
	}{
		{
			name:     "PeerUser",
			peer:     &tg.PeerUser{UserID: 42},
			expected: 42,
		},
		{
			name:     "PeerChat",
			peer:     &tg.PeerChat{ChatID: 7},
			expected: 7,
		},
		{
			name:     "PeerChannel",
			peer:     &tg.PeerChannel{ChannelID: 99},
			expected: 99,
		},
		{
			name:     "nil peer",
			peer:     nil,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := peerChatID(tt.peer)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name            string
		allowedChats    []int64
		allowedUserIDs  []int64
		chatID          int64
		userID          int64
		expectedAllowed bool
	}{
		{
			name:            "both empty, should reject",
			allowedChats:    []int64{},
			allowedUserIDs:  []int64{},
			chatID:          1,
			userID:          1,
			expectedAllowed: false,
		},
		{
			name:            "chat in allowlist",
			allowedChats:    []int64{1, 2, 3},
			allowedUserIDs:  []int64{},
			chatID:          2,
			userID:          99,
			expectedAllowed: true,
		},
		{
			name:            "user in allowlist",
			allowedChats:    []int64{},
			allowedUserIDs:  []int64{10, 20, 30},
			chatID:          99,
			userID:          20,
			expectedAllowed: true,
		},
		{
			name:            "both lists populated, chat matches",
			allowedChats:    []int64{1, 2},
			allowedUserIDs:  []int64{10, 20},
			chatID:          1,
			userID:          99,
			expectedAllowed: true,
		},
		{
			name:            "both lists populated, user matches",
			allowedChats:    []int64{1, 2},
			allowedUserIDs:  []int64{10, 20},
			chatID:          99,
			userID:          10,
			expectedAllowed: true,
		},
		{
			name:            "both lists populated, neither matches",
			allowedChats:    []int64{1, 2},
			allowedUserIDs:  []int64{10, 20},
			chatID:          99,
			userID:          99,
			expectedAllowed: false,
		},
		{
			name:            "negative chat ID (group)",
			allowedChats:    []int64{-100123456},
			allowedUserIDs:  []int64{},
			chatID:          -100123456,
			userID:          1,
			expectedAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
				Silent:         true,
				TracerProvider: otel.GetTracerProvider(),
				Logger:         zap.NewNop(),
				AllowedChats:   tt.allowedChats,
				AllowedUserIDs: tt.allowedUserIDs,
			})
			result := b.isAllowed(tt.chatID, tt.userID)
			require.Equal(t, tt.expectedAllowed, result)
		})
	}
}

func TestNewWarning(t *testing.T) {
	// Just verify that New() doesn't panic with empty allowlists.
	// The warning is logged but we can't easily assert on it without capturing logs.
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedChats:   []int64{},
		AllowedUserIDs: []int64{},
	})
	require.NotNil(t, b)
	require.Empty(t, b.allowedChats)
	require.Empty(t, b.allowedUsers)
}

func TestHandleUsesQueryAnswererWithDefaultTimeout(t *testing.T) {
	a := &captureQueryAnswerer{}
	b := New(context.Background(), nil, a, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})

	answer, err := b.handle(context.Background(), "why is prod red?")
	require.NoError(t, err)
	require.Equal(t, "direct answer", answer.Text)
	require.Equal(t, index.Query{Text: "why is prod red?", Limit: 12}, a.query)
	require.NotZero(t, a.deadline)
	require.Positive(t, time.Until(a.deadline))
	require.LessOrEqual(t, time.Until(a.deadline), defaultAnswerTimeout)
}
