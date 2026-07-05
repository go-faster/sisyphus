package bot

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
)

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
