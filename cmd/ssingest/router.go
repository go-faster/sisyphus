package main

import (
	"time"

	"github.com/go-faster/sisyphus/internal/agentstore"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
	notifyalert "github.com/go-faster/sisyphus/internal/notify/alert"
	notifygitlab "github.com/go-faster/sisyphus/internal/notify/gitlab"
	notifyjira "github.com/go-faster/sisyphus/internal/notify/jira"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
)

// eventRouter builds the event spine every ingestion run and the Alertmanager
// webhook publish to, with the notification gateway subscribed per source and
// — when alertmanager.investigate.enabled is set — the agent subscribed to
// firing alerts.
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
	staleness := notify.Staleness{
		MaxAge: time.Duration(d.cfg.Notify.MaxAssignmentAgeSeconds) * time.Second,
	}

	router := event.NewMux()
	if d.cfg.Alertmanager.Investigate {
		router.Subscribe(event.Subscription{
			Name:    "agent-investigate",
			Sources: []event.Source{event.SourceAlertmanager},
			Handler: agentstore.NewSubscriber(
				agentstore.New(d.services.DB, agentstore.Options{}),
				agentstore.SubscriberOptions{
					MinSeverity: event.Severity(d.cfg.Alertmanager.InvestigateMinSeverity),
				},
			),
		})
	}
	if d.cfg.Alertmanager.Notify {
		// Announcing the alert does not depend on investigating it: the
		// investigation report is a second, later message (and only when
		// alertmanager.investigate is on).
		router.Subscribe(event.Subscription{
			Name:    "notify-alerts",
			Sources: []event.Source{event.SourceAlertmanager},
			Handler: notify.NewBroadcastSubscriber(
				notifyalert.Projector{},
				notify.NewBroadcaster(store, store, notify.ChannelTelegram, nil),
			),
		})
	}
	router.Subscribe(event.Subscription{
		Name:    "notify-gitlab",
		Sources: []event.Source{event.SourceGitLab},
		Handler: notify.NewRouterSubscriber(notifygitlab.Projector{Staleness: staleness}, dispatcher),
	})
	router.Subscribe(event.Subscription{
		Name:    "notify-jira",
		Sources: []event.Source{event.SourceJira},
		Handler: notify.NewRouterSubscriber(notifyjira.Projector{Staleness: staleness}, dispatcher),
	})
	return router
}
