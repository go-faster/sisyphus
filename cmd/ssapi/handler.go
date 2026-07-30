package main

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/go-faster/sisyphus/internal/api"
	"github.com/go-faster/sisyphus/internal/config"
	"github.com/go-faster/sisyphus/internal/ent"
	notifystore "github.com/go-faster/sisyphus/internal/notify/store"
	"github.com/go-faster/sisyphus/internal/tgpeer"
	"github.com/go-faster/sisyphus/internal/wire"
)

func newNotifyStore(db *ent.Client) *notifystore.Store {
	return notifystore.New(db, notifystore.Options{Owner: "ssapi"})
}

// newHandler assembles the ogen handler from the wired components.
//
// The notification store is not optional here even though the handler treats
// it as such: /notify/* backs ssbot's commands (/subscribe, /alerts) and
// /notifications/* backs its delivery drain, and ssbot has no database of its
// own. Without it every one of those calls answers 503 and the whole
// notification path is silently dead — which is what happened until this was
// wired, and why handler_test.go asserts it.
func newHandler(comp wire.Components, version string) *api.Handler {
	return api.New(comp.Retriever, comp.Answerer, version,
		api.WithContentResolver(comp.ContentResolver),
		api.WithURLFetcher(comp.URLFetcher),
		api.WithNotifyStore(newNotifyStore(comp.DB)),
		api.WithPeerStore(tgpeer.New(comp.DB, tgpeer.Options{})),
	)
}

// syncNotifyIdentities reconciles notify.identities into the database.
//
// ssapi does it because it is the only service holding both the config and the
// database. It runs on every start rather than on a migration, so an operator
// edits config.yaml, redeploys, and the mapping is what the file says — and a
// config error fails startup instead of quietly delivering to nobody.
func syncNotifyIdentities(ctx context.Context, db *ent.Client, ids []config.NotifyIdentity) (notifystore.SyncResult, error) {
	out := make([]notifystore.Identity, 0, len(ids))
	for _, id := range ids {
		out = append(out, notifystore.Identity(id))
	}
	res, err := newNotifyStore(db).SyncIdentities(ctx, out)
	if err != nil {
		return res, errors.Wrap(err, "sync notify identities")
	}
	return res, nil
}
