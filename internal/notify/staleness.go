package notify

import "time"

// DefaultMaxAssignmentAge is the cutoff a Projector uses when none is
// configured. A real assignment reaches the projector within one poll
// interval, so a day is generous; what it excludes is history.
const DefaultMaxAssignmentAge = 24 * time.Hour

// Staleness decides whether a membership change (an assignment, a review
// request) is news or history.
//
// The projectors need it because a source event states *current* membership,
// not a change to it: any edit to an issue that has been assigned to you for
// months re-announces that assignment. Outbox dedup normally hides this — the
// second announcement of the same (object, recipient) is suppressed — but the
// first one is not, so an outbox that is new (or a recipient who just got an
// identity) sees a burst of "X assigned you" for assignments made long ago.
//
// The rule is deliberately permissive: over-notifying costs at most one
// message, since the dedup key collapses repeats, while under-notifying
// silently loses a real assignment. So an unknown timestamp always passes —
// a source whose changelog or system notes are unavailable keeps notifying
// rather than going quiet — and only a change we can *prove* is old is
// dropped.
type Staleness struct {
	// MaxAge is how old a membership change may be and still notify. Zero
	// uses DefaultMaxAssignmentAge; negative disables the check entirely.
	MaxAge time.Duration
	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

// Fresh reports whether a change made at changedAt should still notify.
func (s Staleness) Fresh(changedAt time.Time) bool {
	if changedAt.IsZero() {
		return true
	}
	maxAge := s.MaxAge
	switch {
	case maxAge < 0:
		return true
	case maxAge == 0:
		maxAge = DefaultMaxAssignmentAge
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().Sub(changedAt) <= maxAge
}
