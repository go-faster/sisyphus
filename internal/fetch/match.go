package fetch

import (
	"net/http"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-faster/errors"
)

var validMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodHead:   {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

func allowedMethods(methods []string) (map[string]bool, error) {
	if len(methods) == 0 {
		methods = []string{http.MethodGet}
	}
	out := make(map[string]bool, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			continue
		}
		if _, ok := validMethods[method]; !ok {
			return nil, errors.Errorf("unsupported method %q", method)
		}
		out[method] = true
	}
	if len(out) == 0 {
		out[http.MethodGet] = true
	}
	return out, nil
}

func matchPattern(pattern, rawURL string) bool {
	matched, err := doublestar.Match(pattern, rawURL)
	return err == nil && matched
}
