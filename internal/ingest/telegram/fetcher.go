package telegram

import (
	"context"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// RawMessage is the gotd-independent intermediate that the backfiller works on.
type RawMessage struct {
	ChatID     int64
	MessageID  int
	ThreadID   int
	SenderID   int64
	SenderName string
	Text       string
	Date       time.Time
	ReplyToID  int
	RawJSON    []byte
}

// MessageFetcher is the abstraction over gotd MTProto fetch so the backfiller
// is testable without real Telegram.
type MessageFetcher interface {
	// FetchHistory returns up to limit messages for chatID older than (exclusive)
	// beforeMsgID. Messages are returned in DESCENDING message_id order (newest
	// first), matching Telegram's messages.getHistory semantics. If beforeMsgID
	// == 0, fetch newest first.
	// Returns (messages, hasMore, error). hasMore indicates there are older
	// messages still unfetched.
	FetchHistory(ctx context.Context, chatID int64, beforeMsgID int, limit int) ([]RawMessage, bool, error)
}

type gotdFetcher struct {
	client *gotdtelegram.Client
	log    *zap.Logger
}

func newGotdFetcher(client *gotdtelegram.Client, log *zap.Logger) MessageFetcher {
	return &gotdFetcher{client: client, log: log}
}

func (f *gotdFetcher) FetchHistory(ctx context.Context, chatID int64, beforeMsgID, limit int) ([]RawMessage, bool, error) {
	api := tg.NewClient(f.client)

	peer := f.resolvePeer(chatID)

	req := &tg.MessagesGetHistoryRequest{
		Peer:     peer,
		OffsetID: beforeMsgID,
		Limit:    limit,
	}

	result, err := api.MessagesGetHistory(ctx, req)
	if err != nil {
		return nil, false, errors.Wrap(err, "get history")
	}

	modified, ok := result.AsModified()
	if !ok {
		f.log.Warn("telegram: got unmodified messages result, treating as empty")
		return nil, false, nil
	}

	msgs := modified.GetMessages()
	var raw []RawMessage
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
		if !ok {
			f.log.Warn("telegram: skipping non-message type in history",
				zap.String("type", m.TypeName()))
			continue
		}
		raw = append(raw, convertTGMessage(chatID, msg))
	}

	hasMore := len(raw) >= limit
	return raw, hasMore, nil
}

// resolvePeer constructs an InputPeer for the given chat ID.
// TODO: Proper peer resolution requires AccessHash, which is not available
// from the chat ID alone. In production the caller should provide a session
// that has already resolved these peers (e.g. via updates). The zero access
// hash will cause "PEER_ID_INVALID" for most peers. This is a best-effort
// implementation that works for peers whose access hash is known (e.g. from
// a previous resolve step or from the session's internal peer storage).
func (f *gotdFetcher) resolvePeer(chatID int64) tg.InputPeerClass {
	// TODO: Use tg.ContactsResolveUsername if the peer is known by username,
	// or fetch the peer via ChannelsGetChannels / UsersGetUsers.
	// For now, construct the InputPeer based on chat ID convention.
	if chatID < 0 {
		// Supergroup/channel: convention is -100xxxxxx.
		channelID := -chatID
		return &tg.InputPeerChannel{
			ChannelID:  channelID,
			AccessHash: 0, // TODO: needs real access hash
		}
	}
	return &tg.InputPeerUser{
		UserID:     chatID,
		AccessHash: 0, // TODO: needs real access hash
	}
}

func convertTGMessage(chatID int64, msg *tg.Message) RawMessage {
	raw := RawMessage{
		ChatID:    chatID,
		MessageID: msg.ID,
		Text:      msg.GetMessage(),
		Date:      time.Unix(int64(msg.Date), 0),
		RawJSON:   nil, // TODO: serialize msg to JSON for storage
	}

	// Extract sender info.
	if msg.FromID != nil {
		switch peer := msg.FromID.(type) {
		case *tg.PeerUser:
			raw.SenderID = peer.UserID
		case *tg.PeerChannel:
			raw.SenderID = peer.ChannelID
		case *tg.PeerChat:
			// For basic groups, use negative chat ID to distinguish from users.
			raw.SenderID = -peer.ChatID
		}
	}

	// Extract reply-to info.
	if msg.ReplyTo != nil {
		if h, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			if replyID, ok := h.GetReplyToMsgID(); ok {
				raw.ReplyToID = replyID
			}
		}
	}

	return raw
}
