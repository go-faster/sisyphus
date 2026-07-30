package main

import (
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
	notifyinvestigation "github.com/go-faster/sisyphus/internal/notify/investigation"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
)

// newReportRouter builds the spine a finished investigation is published to,
// with the alert chats subscribed as a broadcast destination.
//
// It returns nil when no chat is configured: without a destination there is
// nothing to publish to, and a nil router is what runJobWithTrigger checks.
// The report itself is still persisted on the job row either way.
func newReportRouter(db *ent.Client, cfg config.Config) event.Router {
	targets := alertTargets(cfg.Notify.AlertChats)
	if len(targets) == 0 {
		return nil
	}

	store := notifystore.New(db, notifystore.Options{Owner: "ssagent"})
	broadcaster := notify.NewBroadcaster(store, notify.ChannelTelegram, targets, nil)

	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-alerts",
		Sources: []event.Source{event.SourceAgent},
		Types:   []event.Type{event.TypeInvestigationCompleted},
		Handler: notify.NewBroadcastSubscriber(notifyinvestigation.Projector{}, broadcaster),
	})
	return router
}

func alertTargets(chats []config.AlertChat) []notify.Target {
	targets := make([]notify.Target, 0, len(chats))
	for _, chat := range chats {
		targets = append(targets, notify.Target{
			TelegramUserID:     chat.ID,
			TelegramAccessHash: chat.AccessHash,
			PeerType:           notify.PeerType(chat.Type),
		})
	}
	return targets
}
