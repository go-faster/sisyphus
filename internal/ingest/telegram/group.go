// Package telegram ingests Telegram history via a gotd user session, persists
// raw messages, and groups them into support requests (plan §4).
package telegram

import (
	"sort"
	"strings"
	"time"
)

// Message is a normalized Telegram message used for grouping. It mirrors the
// telegram_messages row but is decoupled from ent so grouping stays pure.
type Message struct {
	ChatID     int64
	MessageID  int64
	SenderID   int64
	SenderName string
	Text       string
	Date       time.Time
	ReplyToID  int64
}

// Conversation is a grouped set of messages forming one candidate support request.
type Conversation struct {
	ChatID         int64
	FirstMessageID int64
	LastMessageID  int64
	Messages       []Message
}

// RawText renders the conversation as a readable transcript (plan §4 raw_text).
func (c Conversation) RawText() string {
	var sb strings.Builder
	for i, m := range c.Messages {
		if i > 0 {
			sb.WriteByte('\n')
		}
		name := m.SenderName
		if name == "" {
			name = "user"
		}
		sb.WriteString(name)
		sb.WriteString(": ")
		sb.WriteString(strings.TrimSpace(m.Text))
	}
	return sb.String()
}

// GroupOptions tunes conversation grouping.
type GroupOptions struct {
	// Window is the max gap between consecutive messages in one conversation.
	Window time.Duration
	// MinMessages drops conversations shorter than this (noise).
	MinMessages int
}

// DefaultGroupOptions matches the plan's 30-60 minute window heuristic.
func DefaultGroupOptions() GroupOptions {
	return GroupOptions{Window: 45 * time.Minute, MinMessages: 1}
}

// Group splits a single chat's messages into conversations using reply chains
// and a time window (plan §4 "conversation grouping"). Input may be unsorted;
// it is sorted by date ascending. Messages must belong to one chat.
func Group(msgs []Message, opts GroupOptions) []Conversation {
	if opts.Window <= 0 {
		opts.Window = DefaultGroupOptions().Window
	}
	if opts.MinMessages <= 0 {
		opts.MinMessages = 1
	}
	if len(msgs) == 0 {
		return nil
	}

	sorted := make([]Message, len(msgs))
	copy(sorted, msgs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Date.Equal(sorted[j].Date) {
			return sorted[i].Date.Before(sorted[j].Date)
		}
		return sorted[i].MessageID < sorted[j].MessageID
	})

	// Map message id -> conversation index, so replies attach to their root.
	convOf := map[int64]int{}
	var convs []Conversation
	var last Message
	cur := -1

	for i, m := range sorted {
		switch {
		case cur >= 0 && m.ReplyToID != 0:
			if idx, ok := convOf[m.ReplyToID]; ok {
				appendMsg(&convs[idx], m)
				convOf[m.MessageID] = idx
				last = m
				continue
			}
			fallthrough
		case cur >= 0 && i > 0 && m.Date.Sub(last.Date) <= opts.Window:
			appendMsg(&convs[cur], m)
			convOf[m.MessageID] = cur
		default:
			convs = append(convs, Conversation{
				ChatID:         m.ChatID,
				FirstMessageID: m.MessageID,
				LastMessageID:  m.MessageID,
				Messages:       []Message{m},
			})
			cur = len(convs) - 1
			convOf[m.MessageID] = cur
		}
		last = m
	}

	out := convs[:0]
	for _, c := range convs {
		if len(c.Messages) >= opts.MinMessages {
			out = append(out, c)
		}
	}
	return out
}

func appendMsg(c *Conversation, m Message) {
	c.Messages = append(c.Messages, m)
	if m.MessageID > c.LastMessageID {
		c.LastMessageID = m.MessageID
	}
	if m.MessageID < c.FirstMessageID {
		c.FirstMessageID = m.MessageID
	}
}
