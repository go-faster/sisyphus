package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActorZero(t *testing.T) {
	assert.True(t, Actor{}.Zero())
	assert.False(t, Actor{Key: "alice"}.Zero())
	assert.False(t, Actor{Display: "Alice"}.Zero())
}

func TestEventAttr(t *testing.T) {
	e := Event{Attributes: map[string]string{"project": "group/proj"}}
	assert.Equal(t, "group/proj", e.Attr("project"))
	assert.Equal(t, "", e.Attr("missing"))
	assert.Equal(t, "", Event{}.Attr("project")) // nil map is safe
}

func TestPayloadRoundTrip(t *testing.T) {
	type mrPayload struct {
		IID       int      `json:"iid"`
		Assignees []string `json:"assignees"`
	}
	in := mrPayload{IID: 42, Assignees: []string{"alice", "bob"}}

	e, err := Event{Source: SourceGitLab, Type: TypeMRUpdated}.WithPayload(in)
	require.NoError(t, err)
	require.NotEmpty(t, e.Payload)

	var out mrPayload
	require.NoError(t, e.DecodePayload(&out))
	assert.Equal(t, in, out)
}

func TestWithPayloadDoesNotMutateReceiver(t *testing.T) {
	base := Event{Source: SourceJira, Type: TypeIssueUpdated}
	_, err := base.WithPayload(map[string]int{"x": 1})
	require.NoError(t, err)
	assert.Nil(t, base.Payload, "WithPayload must return a copy, not mutate the receiver")
}
