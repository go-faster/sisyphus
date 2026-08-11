package gitlab

import (
	"fmt"
	"slices"

	"github.com/go-faster/sisyphus/internal/event"
	ingestgitlab "github.com/go-faster/sisyphus/internal/ingest/gitlab"
	"github.com/go-faster/sisyphus/internal/notify"
)

// projectResolutions fans a merge request's resolved discussion threads out
// into EventMRThreadResolved notifications.
//
// A resolution is addressed to the people the conversation was between — the
// thread's own commenters — plus the MR author, who owns whatever the thread
// asked for whether or not they answered in it. Assignees and reviewers at
// large are deliberately absent: on a review with a dozen threads, telling
// everyone about every one of them is the noise that gets the feature muted,
// and the thread's participants are exactly who was waiting on the answer.
//
// The resolver is never told they resolved their own thread.
//
// Staleness gates it for the same reason it gates a merge: a thread stays
// resolved in every payload from the moment it is closed, so without the
// cutoff the first poll after this shipped would announce every thread
// resolved in the fetched window.
func (pr Projector) projectResolutions(e event.Event, p ingestgitlab.MRPayload) []notify.Event {
	var out []notify.Event
	for _, r := range p.Resolutions {
		if r.ThreadID == "" || !pr.Staleness.Fresh(r.At) {
			continue
		}
		resolver := notify.Actor{
			Source:  notify.SourceGitLab,
			Key:     r.By.Username,
			Display: r.By.Display,
			URL:     r.By.URL,
		}
		url := r.URL
		if url == "" {
			url = e.Subject.URL
		}
		for _, username := range resolutionRecipients(r, p) {
			if username == r.By.Username {
				continue
			}
			out = append(out, notify.Event{
				Source:      notify.SourceGitLab,
				Type:        notify.EventMRThreadResolved,
				Recipient:   notify.Actor{Source: notify.SourceGitLab, Key: username},
				Actor:       resolver,
				Title:       e.Subject.Title,
				Description: notify.Snippet(r.Excerpt),
				Buttons:     []notify.Button{{Text: "Open thread", URL: url}},
				URL:         url,
				ObjectID:    e.Subject.ID,
				// Keyed on the thread, not on a timestamp: a thread reopened
				// and resolved again is the same conversation ending the same
				// way, and the recipient does not need telling twice.
				EventID:    fmt.Sprintf("gitlab_mr_thread_resolved:%s:%s:%s", e.Subject.ID, r.ThreadID, username),
				OccurredAt: r.At,
			})
		}
	}
	return out
}

// resolutionRecipients are a resolved thread's participants, oldest comment
// first, then the merge request's author. Deduped, so an author who also
// commented in the thread is told once.
func resolutionRecipients(r ingestgitlab.Resolution, p ingestgitlab.MRPayload) []string {
	out := make([]string, 0, len(r.Participants)+1)
	for _, u := range r.Participants {
		if u.Username != "" && !slices.Contains(out, u.Username) {
			out = append(out, u.Username)
		}
	}
	if p.Author.Username != "" && !slices.Contains(out, p.Author.Username) {
		out = append(out, p.Author.Username)
	}
	return out
}
