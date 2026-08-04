package sheets

import (
	"regexp"
	"strings"

	"github.com/go-faster/errors"
)

// urlPath matches the /spreadsheets/d/<id> segment of a Google Sheets URL.
var urlPath = regexp.MustCompile(`/spreadsheets/d/([a-zA-Z0-9_-]+)`)

// bareID matches a spreadsheet ID on its own.
var bareID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ParseID extracts the spreadsheet ID from a Google Sheets URL, or accepts an
// ID as-is. Callers hand it whatever the user pasted.
func ParseID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty spreadsheet reference")
	}
	if m := urlPath.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	if !bareID.MatchString(s) {
		return "", errors.Errorf("not a spreadsheet URL or id: %q", s)
	}
	return s, nil
}

// resolve turns a caller-supplied spreadsheet reference into an ID, applying
// the default and the allowlist.
func (c *Client) resolve(ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		if c.def == "" {
			return "", errors.New("no spreadsheet given and no default configured")
		}
		return c.def, nil
	}
	id, err := ParseID(ref)
	if err != nil {
		return "", err
	}
	if len(c.allow) > 0 && !c.allow[id] {
		return "", errors.Errorf("spreadsheet %s is not in the allowlist", id)
	}
	return id, nil
}
