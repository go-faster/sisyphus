package qdrant

import (
	"testing"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// TestPayloadKeepsTypedCollections is the regression this file exists for.
//
// index.Chunk.Metadata is map[string]any, so nothing stops a caller storing a
// []string or a map[string]string in it — the GitLab and Jira document metadata
// carry labels, components and assignees exactly that way, and chunkers copy
// document metadata through verbatim. qdrant.NewValue has no case for either,
// and addPayloadValue used to discard the error, so those fields never reached
// Qdrant at all. No error, no log: the only symptom was a keyword filter on
// `labels` matching nothing, forever.
func TestPayloadKeepsTypedCollections(t *testing.T) {
	payload, dropped := chunkToPayload(index.Chunk{
		ID: uuid.New(),
		Metadata: map[string]any{
			"labels":     []string{"bug", "auth"},
			"components": []string{"api"},
			"nested":     map[string]string{"team": "platform"},
			"assignees":  []any{"alice"},
		},
	})
	require.Empty(t, dropped)

	require.Equal(t, []string{"bug", "auth"}, listStrings(t, payload["labels"]))
	require.Equal(t, []string{"api"}, listStrings(t, payload["components"]))
	require.Equal(t, []string{"alice"}, listStrings(t, payload["assignees"]))

	nested := payload["nested"].GetStructValue()
	require.NotNil(t, nested)
	require.Equal(t, "platform", nested.Fields["team"].GetStringValue())
}

// TestPayloadPreservesPrimitiveTypes pins that the JSON normalization added for
// typed collections does not reach the primitives. An int must stay a Qdrant
// integer: routing it through JSON would make it a double, changing the type of
// every numeric payload field already written.
func TestPayloadPreservesPrimitiveTypes(t *testing.T) {
	payload, dropped := chunkToPayload(index.Chunk{
		ID: uuid.New(),
		Metadata: map[string]any{
			"iid":     42,
			"chat_id": int64(-1001234567890),
			"score":   1.5,
			"draft":   true,
			"state":   "opened",
			"absent":  nil,
		},
	})
	require.Empty(t, dropped)

	require.Equal(t, int64(42), payload["iid"].GetIntegerValue())
	require.Equal(t, int64(-1001234567890), payload["chat_id"].GetIntegerValue())
	require.InDelta(t, 1.5, payload["score"].GetDoubleValue(), 0)
	require.True(t, payload["draft"].GetBoolValue())
	require.Equal(t, "opened", payload["state"].GetStringValue())
	require.Equal(t, qdrant.NullValue_NULL_VALUE, payload["absent"].GetNullValue())
}

// TestPayloadReportsUnconvertible pins that a value JSON genuinely cannot
// express is reported rather than silently lost.
func TestPayloadReportsUnconvertible(t *testing.T) {
	payload, dropped := chunkToPayload(index.Chunk{
		ID: uuid.New(),
		Metadata: map[string]any{
			"ok":  "fine",
			"bad": make(chan int),
		},
	})
	require.Equal(t, []string{"bad"}, dropped)
	require.Contains(t, payload, "ok")
	require.NotContains(t, payload, "bad")
}

// TestPayloadSanitizesInvalidUTF8 pins that the UTF-8 scrubbing still applies
// after the switch was reorganized — Qdrant rejects an invalid string outright,
// which would drop the field.
func TestPayloadSanitizesInvalidUTF8(t *testing.T) {
	payload, dropped := chunkToPayload(index.Chunk{
		ID:    uuid.New(),
		Title: "bad\xff title",
		Metadata: map[string]any{
			"key\xffword": "val\xffue",
		},
	})
	require.Empty(t, dropped)
	require.Equal(t, "bad title", payload["title"].GetStringValue())
	require.Equal(t, "value", payload["keyword"].GetStringValue())
}

func TestSanitizeStructValue(t *testing.T) {
	type issue struct {
		Key    string   `json:"key"`
		Labels []string `json:"labels"`
	}
	got := sanitizePayloadValue(issue{Key: "ABC-1", Labels: []string{"bug"}})
	m, ok := got.(map[string]any)
	require.True(t, ok, "a struct must normalize to a map, got %T", got)
	require.Equal(t, "ABC-1", m["key"])
	require.Equal(t, []any{"bug"}, m["labels"])
}

func listStrings(t *testing.T, v *qdrant.Value) []string {
	t.Helper()
	require.NotNil(t, v)
	list := v.GetListValue()
	require.NotNil(t, list, "expected a list value")
	out := make([]string, 0, len(list.Values))
	for _, item := range list.Values {
		out = append(out, item.GetStringValue())
	}
	return out
}
