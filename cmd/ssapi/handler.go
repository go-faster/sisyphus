package main

import (
	"github.com/go-faster/sisyphus/internal/api"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/wire"
)

// newHandler assembles the ogen handler from the wired components.
//
// The notification store is not optional here even though the handler treats
// it as such: /notify/* backs ssbot's commands (/link, /subscribe, /alerts)
// and /notifications/* backs its delivery drain, and ssbot has no database of
// its own. Without it every one of those calls answers 503 and the whole
// notification path is silently dead — which is what happened until this was
// wired, and why handler_test.go asserts it.
func newHandler(comp wire.Components, version string) *api.Handler {
	return api.New(comp.Retriever, comp.Answerer, version,
		api.WithContentResolver(comp.ContentResolver),
		api.WithURLFetcher(comp.URLFetcher),
		api.WithNotifyStore(notifystore.New(comp.DB, notifystore.Options{Owner: "ssapi"})),
	)
}
