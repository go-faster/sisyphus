package event

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionMatches(t *testing.T) {
	mr := Event{Source: SourceGitLab, Type: TypeMRUpdated}
	issue := Event{Source: SourceJira, Type: TypeIssueUpdated}

	tests := []struct {
		name string
		sub  Subscription
		e    Event
		want bool
	}{
		{"empty matches all", Subscription{}, mr, true},
		{"source match", Subscription{Sources: []Source{SourceGitLab}}, mr, true},
		{"source mismatch", Subscription{Sources: []Source{SourceGitLab}}, issue, false},
		{"type match", Subscription{Types: []Type{TypeMRUpdated}}, mr, true},
		{"type mismatch", Subscription{Types: []Type{TypeMRUpdated}}, issue, false},
		{"source+type match", Subscription{Sources: []Source{SourceGitLab}, Types: []Type{TypeMRUpdated}}, mr, true},
		{"source ok type not", Subscription{Sources: []Source{SourceGitLab}, Types: []Type{TypeReleased}}, mr, false},
		{"multi-source", Subscription{Sources: []Source{SourceJira, SourceGitLab}}, mr, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.sub.matches(tt.e))
		})
	}
}

func TestMuxRouteFanOut(t *testing.T) {
	m := NewMux()

	var gotAll, gotGitLab, gotJira int
	m.Subscribe(Subscription{Name: "all", Handler: HandlerFunc(func(context.Context, Event) error {
		gotAll++
		return nil
	})})
	m.Subscribe(Subscription{Name: "gitlab", Sources: []Source{SourceGitLab}, Handler: HandlerFunc(func(context.Context, Event) error {
		gotGitLab++
		return nil
	})})
	m.Subscribe(Subscription{Name: "jira", Sources: []Source{SourceJira}, Handler: HandlerFunc(func(context.Context, Event) error {
		gotJira++
		return nil
	})})

	require.NoError(t, m.Route(context.Background(), Event{Source: SourceGitLab, Type: TypeMRUpdated}))
	require.NoError(t, m.Route(context.Background(), Event{Source: SourceJira, Type: TypeIssueUpdated}))

	assert.Equal(t, 2, gotAll, "match-all handler sees both events")
	assert.Equal(t, 1, gotGitLab)
	assert.Equal(t, 1, gotJira)
}

func TestMuxRouteNoMatchIsNoError(t *testing.T) {
	m := NewMux()
	m.Subscribe(Subscription{Sources: []Source{SourceGitLab}, Handler: HandlerFunc(func(context.Context, Event) error {
		t.Fatal("handler must not be called for a non-matching event")
		return nil
	})})
	assert.NoError(t, m.Route(context.Background(), Event{Source: SourceJira}))
}

func TestMuxRouteAggregatesErrorsAndContinues(t *testing.T) {
	m := NewMux()
	sentinel := errors.New("boom")
	var secondCalled bool

	m.Subscribe(Subscription{Name: "failing", Handler: HandlerFunc(func(context.Context, Event) error {
		return sentinel
	})})
	m.Subscribe(Subscription{Name: "healthy", Handler: HandlerFunc(func(context.Context, Event) error {
		secondCalled = true
		return nil
	})})

	err := m.Route(context.Background(), Event{Source: SourceGitLab})
	require.Error(t, err)
	assert.True(t, secondCalled, "a failing handler must not stop later handlers")
	assert.ErrorIs(t, err, sentinel, "original error stays inspectable through the wrapper")
	assert.Contains(t, err.Error(), "failing", "error is attributed to its subscription")
}
