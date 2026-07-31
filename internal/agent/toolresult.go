package agent

import (
	"fmt"
	"unicode/utf8"
)

// defaultMaxToolResultBytes bounds a single tool result before it enters the
// conversation. It is a safety valve against a pathological result, not a
// quality lever: a tool result is appended to the conversation and then
// re-sent on every subsequent iteration, so one oversized result is paid for
// once per remaining iteration, not once.
//
// The size is chosen to leave ordinary results untouched. A 30-result
// search_knowledge response runs ~64KB and is legitimately worth its context;
// what this exists to catch is the unbounded kind — a browser accessibility
// snapshot, a fetched page, raw shell output from the sandbox — where a single
// call has been observed adding ~55k tokens to the prompt and then riding
// along in every later request. Lowering a chatty tool's own result size (e.g.
// search_knowledge's default limit) is the better fix where one exists; this
// only guarantees a ceiling.
const defaultMaxToolResultBytes = 64 << 10

// truncateToolResult caps s at maxBytes, cutting on a rune boundary so the
// result stays valid UTF-8, and appends a marker naming how much was dropped.
//
// The marker matters: silently truncating leaves the model reasoning over a
// half-record as if it were the whole answer. Told that output was cut, it can
// re-query more narrowly instead.
//
// Callers must extract anything they need from the *full* result before
// truncating — see coreLoop, which runs collectURLs first. Truncation can cut
// mid-JSON, and collectURLs only reads structured keys from parseable JSON, so
// a truncated result would silently yield no URLs at all.
func truncateToolResult(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}

	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	dropped := len(s) - cut
	return fmt.Sprintf("%s\n[truncated: %d of %d bytes omitted; narrow the query or arguments to see the rest]",
		s[:cut], dropped, len(s)), true
}
