package main

import (
	"github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/event"
	"github.com/go-faster/sisyphus/internal/notify"
	notifyinvestigation "github.com/go-faster/sisyphus/internal/notify/investigation"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
)

// newReportRouter builds the spine a finished investigation is published to,
// with the registered alert chats subscribed as a broadcast destination.
//
// The destination list is read per event from the notify store, not from
// config: a chat registers itself when someone runs /alerts on inside it, so
// chats come and go without a redeploy. With none registered the broadcast is
// a no-op and the report still lands on the job row.
func newReportRouter(db *ent.Client) event.Router {
	store := notifystore.New(db, notifystore.Options{Owner: "ssagent"})
	broadcaster := notify.NewBroadcaster(store, store, notify.ChannelTelegram, nil)

	router := event.NewMux()
	router.Subscribe(event.Subscription{
		Name:    "notify-alerts",
		Sources: []event.Source{event.SourceAgent},
		Types:   []event.Type{event.TypeInvestigationCompleted},
		Handler: notify.NewBroadcastSubscriber(notifyinvestigation.Projector{}, broadcaster),
	})
	return router
}
