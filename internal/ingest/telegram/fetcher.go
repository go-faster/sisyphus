package telegram

import (
	"context"
	"encoding/json"
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
	client     *gotdtelegram.Client
	log        *zap.Logger
	peers      map[int64]int64
	peersReady bool
}

func newGotdFetcher(client *gotdtelegram.Client, log *zap.Logger) MessageFetcher {
	return &gotdFetcher{client: client, log: log}
}

// peerBootstrapper is implemented by fetchers that can pre-resolve peer access hashes.
type peerBootstrapper interface {
	bootstrapPeers(ctx context.Context, chatIDs []int64) error
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
		raw = append(raw, convertTGMessage(chatID, msg, f.log))
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
	if f.peers != nil {
		if h, ok := f.peers[chatID]; ok {
			if chatID < 0 {
				channelID := -chatID
				return &tg.InputPeerChannel{
					ChannelID:  channelID,
					AccessHash: h,
				}
			}
			return &tg.InputPeerUser{
				UserID:     chatID,
				AccessHash: h,
			}
		}
	}
	// Fallback to zero access hash (will fail downstream for most peers).
	if chatID < 0 {
		channelID := -chatID
		return &tg.InputPeerChannel{
			ChannelID:  channelID,
			AccessHash: 0,
		}
	}
	return &tg.InputPeerUser{
		UserID:     chatID,
		AccessHash: 0,
	}
}

func (f *gotdFetcher) bootstrapPeers(ctx context.Context, chatIDs []int64) error {
	api := tg.NewClient(f.client)

	req := &tg.MessagesGetDialogsRequest{
		OffsetDate: 0,
		OffsetID:   0,
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      200,
		Hash:       0,
	}

	result, err := api.MessagesGetDialogs(ctx, req)
	if err != nil {
		return errors.Wrap(err, "get dialogs")
	}

	modified, ok := result.AsModified()
	if !ok {
		f.log.Warn("telegram: got unmodified dialogs result")
		f.peersReady = true
		return nil
	}

	peers := make(map[int64]int64)

	for _, c := range modified.GetChats() {
		switch v := c.(type) {
		case *tg.Channel:
			chatID := -(v.ID + 1000000000000)
			peers[chatID] = v.AccessHash
		case *tg.Chat:
			// Basic group: no access hash. Use InputPeerChat later if needed.
			// For now we store 0 to indicate "no hash".
			peers[v.ID] = 0
		case *tg.ChatForbidden, *tg.ChannelForbidden:
			continue
		}
	}

	for _, u := range modified.GetUsers() {
		if v, ok := u.(*tg.User); ok {
			peers[v.ID] = v.AccessHash
		}
	}

	for _, id := range chatIDs {
		if _, ok := peers[id]; !ok {
			f.log.Warn("telegram: peer not in dialogs; fetch will fail with PEER_ID_INVALID",
				zap.Int64("chat_id", id))
		}
	}

	f.peers = peers
	f.peersReady = true
	return nil
}

func convertTGMessage(chatID int64, msg *tg.Message, log *zap.Logger) RawMessage {
	raw := RawMessage{
		ChatID:    chatID,
		MessageID: msg.ID,
		Text:      msg.GetMessage(),
		Date:      time.Unix(int64(msg.Date), 0),
		RawJSON:   nil,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		if log != nil {
			log.Warn("telegram: marshal message", zap.Int("msg_id", msg.ID), zap.Error(err))
		}
	} else {
		raw.RawJSON = data
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
