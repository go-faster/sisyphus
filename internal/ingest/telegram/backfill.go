package telegram

import (
	"cmp"
	"context"
	"slices"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	gotdtelegram "github.com/gotd/td/telegram"

	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/index"
)

const defaultPageSize = 100

// ChatSpec identifies one monitored chat to backfill.
type ChatSpec struct {
	ID       int64  // for supergroups/chat IDs: -100xxxxxxxxx form; for users: positive user id
	Username string // optional human-readable username, for logging
	Limit    int    // per-chat max messages for this backfill; 0 = unlimited within the backfill window
}

// BackfillRequest describes a single backfill run.
type BackfillRequest struct {
	Chats  []ChatSpec
	Cursor Cursor // resume point per chat: {chat_id -> last_msg_id}
	Limit  int    // per-chats overall cap (applies across all chats if set); 0 = no cap
}

// BackfillResult summarizes one backfill run.
type BackfillResult struct {
	Documents     []index.Document
	NextCursor    Cursor
	TotalMessages int
	TotalConvos   int
}

// BackfillOptions configures the Backfiller.
type BackfillOptions struct {
	Session *gotdtelegram.Client // gotd user-session client
	Fetcher MessageFetcher       // test injection; if set, Session is ignored
}

// Backfiller ingests Telegram chat history via gotd and persists it to ent.
type Backfiller struct {
	db      *ent.Client
	fetcher MessageFetcher
}

// NewBackfiller creates a new Backfiller. Returns an error if neither Session
// nor Fetcher is configured.
func NewBackfiller(db *ent.Client, opts BackfillOptions) (*Backfiller, error) {
	var fetcher MessageFetcher
	switch {
	case opts.Fetcher != nil:
		fetcher = opts.Fetcher
	case opts.Session != nil:
		fetcher = newGotdFetcher(opts.Session)
	default:
		return nil, errors.New("telegram: session not configured")
	}

	return &Backfiller{
		db:      db,
		fetcher: fetcher,
	}, nil
}

// Backfill fetches history for each chat in req, persists messages, groups
// them into conversations, persists support_requests, and returns Documents.
func (b *Backfiller) Backfill(ctx context.Context, req BackfillRequest) (BackfillResult, error) {
	lg := zctx.From(ctx)
	sorted := make([]ChatSpec, len(req.Chats))
	copy(sorted, req.Chats)
	slices.SortStableFunc(sorted, func(a, b ChatSpec) int {
		return cmp.Compare(a.ID, b.ID)
	})

	if pb, ok := b.fetcher.(peerBootstrapper); ok {
		chatIDs := make([]int64, len(sorted))
		for i, c := range sorted {
			chatIDs[i] = c.ID
		}
		if err := pb.bootstrapPeers(ctx, chatIDs); err != nil {
			lg.Warn("telegram: peer bootstrap failed; fetches may fail with PEER_ID_INVALID", zap.Error(err))
		}
	}

	result := BackfillResult{
		NextCursor: Cursor{PerChat: make(map[int64]int)},
	}
	totalFetched := 0

	for _, chat := range sorted {
		lg.Info("backfilling chat",
			zap.Int64("chat_id", chat.ID),
			zap.String("username", chat.Username))

		minID := req.Cursor.PerChat[chat.ID]
		chatLimit := chat.Limit
		if req.Limit > 0 {
			remaining := req.Limit - totalFetched
			if remaining <= 0 {
				break
			}
			if chatLimit == 0 || chatLimit > remaining {
				chatLimit = remaining
			}
		}

		var chatMsgs []RawMessage
		offsetID := 0
		for {
			pageLimit := defaultPageSize
			if chatLimit > 0 {
				remaining := chatLimit - len(chatMsgs)
				if remaining <= 0 {
					break
				}
				if remaining < pageLimit {
					pageLimit = remaining
				}
			}

			msgs, hasMore, err := b.fetcher.FetchHistory(ctx, chat.ID, minID, offsetID, pageLimit)
			if err != nil {
				return result, errors.Wrap(err, "telegram fetch")
			}

			if len(msgs) == 0 {
				break
			}

			if err := persistMessages(ctx, b.db, msgs); err != nil {
				return result, errors.Wrap(err, "persist messages")
			}

			chatMsgs = append(chatMsgs, msgs...)
			offsetID = msgs[len(msgs)-1].MessageID

			if !hasMore {
				break
			}
		}

		if len(chatMsgs) == 0 {
			lg.Debug("no new messages for chat", zap.Int64("chat_id", chat.ID))
			continue
		}

		// Convert RawMessages to group.Message.
		groupMsgs := rawMessagesToMessages(chatMsgs)
		convs := Group(groupMsgs, DefaultGroupOptions())

		if err := persistSupportRequests(ctx, b.db, chat.ID, convs); err != nil {
			return result, errors.Wrap(err, "persist support requests")
		}

		for _, conv := range convs {
			doc := DocumentFromConversation(conv, "", "")
			result.Documents = append(result.Documents, doc)
		}

		// Record the newest successfully fetched message ID as the cursor.
		// The first entry in chatMsgs is the newest (descending order).
		newestID := chatMsgs[0].MessageID
		result.NextCursor.PerChat[chat.ID] = newestID

		result.TotalMessages += len(chatMsgs)
		result.TotalConvos += len(convs)
		totalFetched += len(chatMsgs)

		lg.Info("chat backfill complete",
			zap.Int64("chat_id", chat.ID),
			zap.Int("messages", len(chatMsgs)),
			zap.Int("conversations", len(convs)))
	}

	return result, nil
}

// rawMessagesToMessages converts the internal RawMessage type to the
// grouping.Message type used by Group().
func rawMessagesToMessages(raw []RawMessage) []Message {
	msgs := make([]Message, len(raw))
	for i, r := range raw {
		m := Message{
			ChatID:     r.ChatID,
			MessageID:  int64(r.MessageID),
			SenderID:   r.SenderID,
			SenderName: r.SenderName,
			Text:       r.Text,
			Date:       r.Date,
			ReplyToID:  int64(r.ReplyToID),
		}
		msgs[i] = m
	}
	return msgs
}
