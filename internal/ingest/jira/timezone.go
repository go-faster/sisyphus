package jira

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
)

// location returns the timezone JQL date bounds must be rendered in: the
// authenticated user's, which is what Jira evaluates a bare "yyyy-MM-dd HH:mm"
// literal against.
//
// It is resolved once per Fetcher. CheckAuth already fetches /myself and seeds
// it via cacheLocation, so the common path (preflight then fetch) costs no extra
// request; a Fetcher used without CheckAuth resolves it here instead. Falls back
// to UTC, which is the pre-existing behavior.
func (f *Fetcher) location(ctx context.Context) *time.Location {
	f.tzOnce.Do(func() {
		f.tz = time.UTC

		req, err := f.buildAPIRequest(ctx, "/rest/api/2/myself")
		if err != nil {
			return
		}
		body, err := f.doRequest(req, "jira timezone lookup")
		if err != nil {
			zctx.From(ctx).Warn("jira: timezone lookup failed, using UTC for JQL bounds",
				zap.Error(err))
			return
		}
		var user jiraUserResponse
		if err := json.Unmarshal(body, &user); err != nil {
			zctx.From(ctx).Warn("jira: cannot parse timezone lookup, using UTC for JQL bounds",
				zap.Error(err))
			return
		}
		f.setLocation(ctx, user.TimeZone)
	})
	return f.tz
}

// cacheLocation seeds the timezone from a /myself response the caller already
// has, so location does not issue a second request for it.
func (f *Fetcher) cacheLocation(ctx context.Context, name string) {
	f.tzOnce.Do(func() {
		f.tz = time.UTC
		f.setLocation(ctx, name)
	})
}

// setLocation parses an IANA timezone name onto the Fetcher, leaving the
// existing value in place if it cannot be resolved. Only call from inside
// tzOnce.
func (f *Fetcher) setLocation(ctx context.Context, name string) {
	if name == "" {
		zctx.From(ctx).Warn("jira: no timezone reported, using UTC for JQL bounds")
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		zctx.From(ctx).Warn("jira: unknown timezone, using UTC for JQL bounds",
			zap.String("timezone", name), zap.Error(err))
		return
	}
	f.tz = loc
}
