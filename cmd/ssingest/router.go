package main

import (
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
	notifygitlab "github.com/go-faster/sisyphus/internal/notify/gitlab"
	notifyjira "github.com/go-faster/sisyphus/internal/notify/jira"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
)

// eventRouter builds the event spine every ingestion run publishes to, with
// the notification gateway subscribed per source.
//
// This is what makes one poll serve both destinations: the GitLab/Jira source
// adapters fetch once, hand each item to the knowledge-graph indexer and to
// this router, and Notify projects the same event into per-recipient outbox
// rows. There is no second fetcher, no second cursor and no notify-only poll
// cadence any more.
//
// A synchronous in-process Mux for now; a durable queue-backed Router can
// replace it without adapters or subscribers changing.
func (d *ingestDeps) eventRouter() event.Router {
	store := notifystore.New(d.services.DB, notifystore.Options{Owner: "ssingest"})
	dispatcher := notify.NewDispatcher(store, store, notify.ChannelTelegram, nil)

	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-gitlab",
		Sources: []event.Source{event.SourceGitLab},
		Handler: notify.NewRouterSubscriber(notifygitlab.Projector{}, dispatcher),
	})
	router.Subscribe(event.Subscription{
		Name:    "notify-jira",
		Sources: []event.Source{event.SourceJira},
		Handler: notify.NewRouterSubscriber(notifyjira.Projector{}, dispatcher),
	})
	return router
}
