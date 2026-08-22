// Package sheets is a small Google Sheets client: read a range, write a range,
// append rows, clear a range, describe the tabs.
//
// It authenticates as a service account, so the only spreadsheets it can touch
// are the ones explicitly shared with that account's address. [Options.Allow]
// narrows that further, and [Options.ReadOnly] takes away the write half.
package sheets

import (
	"context"
	"net/http"

	"github.com/go-faster/errors"
	"go.uber.org/zap"
	"google.golang.org/api/option"
	gsheets "google.golang.org/api/sheets/v4"
	htransport "google.golang.org/api/transport/http"

	"github.com/go-faster/sisyphus/internal/netclient"
)

// Options configures a [Client].
type Options struct {
	Logger *zap.Logger

	// CredentialsFile is the path to a service account JSON key. Empty falls
	// back to Application Default Credentials.
	CredentialsFile string

	// Default is the spreadsheet (URL or ID) used by calls that name none.
	Default string

	// Allow limits which spreadsheets may be addressed, by URL or ID. Empty
	// means any spreadsheet the credentials can reach.
	Allow []string

	// ReadOnly requests a read-only OAuth scope and refuses every mutating
	// call locally, so a misconfigured caller fails here rather than at Google.
	ReadOnly bool
}

func (opts *Options) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// Client talks to the Google Sheets API.
type Client struct {
	svc      *gsheets.SpreadsheetsService
	lg       *zap.Logger
	def      string
	allow    map[string]bool
	readOnly bool
}

// New builds a Client. It performs no network I/O beyond loading credentials.
func New(ctx context.Context, opts Options) (*Client, error) {
	opts.setDefaults()

	c := &Client{
		lg:       opts.Logger,
		readOnly: opts.ReadOnly,
	}
	if len(opts.Allow) > 0 {
		c.allow = make(map[string]bool, len(opts.Allow))
		for _, ref := range opts.Allow {
			id, err := ParseID(ref)
			if err != nil {
				return nil, errors.Wrap(err, "allow")
			}
			c.allow[id] = true
		}
	}
	if opts.Default != "" {
		id, err := ParseID(opts.Default)
		if err != nil {
			return nil, errors.Wrap(err, "default spreadsheet")
		}
		if c.allow != nil && !c.allow[id] {
			return nil, errors.Errorf("default spreadsheet %s is not in the allowlist", id)
		}
		c.def = id
	}

	scope := gsheets.SpreadsheetsScope
	if opts.ReadOnly {
		scope = gsheets.SpreadsheetsReadonlyScope
	}
	clientOpts := []option.ClientOption{option.WithScopes(scope)}
	if opts.CredentialsFile != "" {
		// Pinned to ServiceAccount rather than the generic credentials-file
		// option: the key is operator-supplied, and refusing every other
		// credential shape keeps a swapped-in file from silently
		// authenticating as something else.
		clientOpts = append(clientOpts, option.WithAuthCredentialsFile(option.ServiceAccount, opts.CredentialsFile))
	}

	// Google's authenticating transport goes in as netclient middleware, so
	// Sheets calls report like every other outbound client. Handing the
	// credential options to NewService instead would leave the transport it
	// builds internally uninstrumented, and WithHTTPClient makes NewService
	// skip auth entirely.
	httpClient, err := netclient.HTTPClient(ctx, "sheets", "", netclient.HTTPClientOptions{
		Wrap: func(base http.RoundTripper) (http.RoundTripper, error) {
			return htransport.NewTransport(ctx, base, clientOpts...)
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "sheets http client")
	}

	svc, err := gsheets.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, errors.Wrap(err, "sheets service")
	}
	c.svc = svc.Spreadsheets
	return c, nil
}

// Default reports the configured default spreadsheet ID, if any.
func (c *Client) Default() string { return c.def }

// ReadOnly reports whether mutating calls are refused.
func (c *Client) ReadOnly() bool { return c.readOnly }

func (c *Client) checkWritable() error {
	if c.readOnly {
		return errors.New("client is read-only")
	}
	return nil
}
