package telegram

import (
	"encoding/json"

	"github.com/go-faster/errors"
)

// Cursor stores per-chat backfill progress as chat_id -> last backfilled
// message_id. Zero value is an empty cursor (resume from newest).
type Cursor struct {
	PerChat map[int64]int `json:"per_chat"`
}

// Encode serializes the cursor to a JSON string.
func (c Cursor) Encode() (string, error) {
	if c.PerChat == nil {
		c.PerChat = map[int64]int{}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", errors.Wrap(err, "marshal cursor")
	}
	return string(data), nil
}

// DecodeCursor deserializes a cursor from a JSON string.
func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return Cursor{}, errors.Wrap(err, "unmarshal cursor")
	}
	if c.PerChat == nil {
		c.PerChat = map[int64]int{}
	}
	return c, nil
}
