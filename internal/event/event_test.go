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

	e, err := Event{Source: SourceGitLab, Type: TypeMRUpdated}.WithPayload(3, in)
	require.NoError(t, err)
	require.NotEmpty(t, e.Payload)
	assert.Equal(t, 3, e.PayloadVersion, "WithPayload stamps the version it was given")

	var out mrPayload
	require.NoError(t, e.DecodePayload(3, &out))
	assert.Equal(t, in, out)
}

// A reader that does not understand the shape gets told which shapes are in
// play, rather than a decode error that names no cause — or, where the JSON
// happens to fit, no error at all.
func TestDecodePayloadRejectsOtherVersion(t *testing.T) {
	e, err := Event{Source: SourceGitLab, Type: TypeMRUpdated}.WithPayload(1, map[string]int{"iid": 42})
	require.NoError(t, err)

	var out map[string]int
	err = e.DecodePayload(2, &out)
	require.Error(t, err)
	var verr *PayloadVersionError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, 1, verr.Got)
	assert.Equal(t, 2, verr.Want)
	assert.Equal(t, SourceGitLab, verr.Source)
	assert.Equal(t, TypeMRUpdated, verr.Type)
	assert.Empty(t, out, "a rejected payload is not decoded into v")
}

// An event written before payloads were versioned reads as version 0, which no
// reader declares — the point being that an unstamped payload is exactly the
// one whose shape nothing vouches for.
func TestDecodePayloadRejectsUnversioned(t *testing.T) {
	e := Event{Source: SourceJira, Type: TypeIssueUpdated, Payload: []byte(`{"iid":42}`)}

	var out map[string]int
	require.ErrorAs(t, e.DecodePayload(1, &out), new(*PayloadVersionError))
}

func TestWithPayloadDoesNotMutateReceiver(t *testing.T) {
	base := Event{Source: SourceJira, Type: TypeIssueUpdated}
	_, err := base.WithPayload(1, map[string]int{"x": 1})
	require.NoError(t, err)
	assert.Nil(t, base.Payload, "WithPayload must return a copy, not mutate the receiver")
	assert.Zero(t, base.PayloadVersion)
}
