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
// The size is set from measured results against a live gateway, above the
// largest legitimate one and below the pathological kind:
//
//	search_knowledge limit=30    133,352 B   the biggest result worth keeping whole
//	search_knowledge limit=12     20,009 B
//	browser_snapshot (typical)    26–51 KB   docs pages, a GitHub issue list
//	browser_snapshot (Wikipedia) 487,753 B   what this exists to catch
//
// Measured in a deployed run, the 487KB snapshot above truncates to 196,608
// bytes and lands as ~48.6k prompt tokens — roughly 4 bytes/token, so untrimmed
// it would have cost ~120k tokens, and then ridden along in every later request.
//
// Size the cap in bytes, from a real result. Deriving it from token counts is
// how an earlier revision landed on 64KB: that number came from dividing one
// run's bytes by a different run's tokens, and it would have halved a
// legitimate search response while missing most snapshots.
//
// Lowering a chatty tool's own result size is the better fix where one exists:
// search_knowledge's default limit of 30 costs 133KB per call where limit=12
// costs 20KB. This only guarantees a ceiling.
const defaultMaxToolResultBytes = 192 << 10

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
