package anydoc

import "strings"

// Codes a conversion can fail with. The first six mirror anydoc's own
// ConvertError variants, which it documents as stable, machine-readable
// names; the rest name failures of the subprocess itself.
const (
	CodeUnsupported   = "unsupported"   // unknown format, or one with no text to extract (a scanned PDF)
	CodeMalformed     = "malformed"     // structurally unusable
	CodeEncrypted     = "encrypted"     // password-protected
	CodeResourceLimit = "resourceLimit" // crossed a decompression/nesting/expansion limit
	CodeMissingPart   = "missingPart"   // a part required for any output is absent
	CodeIO            = "io"            // the child could not read the file
	CodeTimeout       = "timeout"       // the conversion outran its deadline
	CodeTooLarge      = "tooLarge"      // the Markdown outgrew the output cap
	CodeCrashed       = "crashed"       // the child died, or failed in a way it does not describe
	CodeUnknown       = "unknown"       // anydoc refused it in words this does not recognize
)

// Error is one document anydoc could not turn into Markdown. It is always
// per-document: the caller counts it, skips the file, and keeps walking.
type Error struct {
	// Code is one of the Code constants.
	Code string
	// Message is what the converter said.
	Message string
}

func (e *Error) Error() string { return "convert document (" + e.Code + "): " + e.Message }

// classify maps an anydoc error message onto its Code.
//
// The CLI prints ConvertError's Display text, not its code(), so the prefix
// is what there is to match on. A message this does not recognize still
// skips the document - the code only labels a log line and a metric, so
// getting it wrong costs nothing but the label.
func classify(message string) string {
	switch {
	case strings.HasPrefix(message, "unsupported input:"):
		return CodeUnsupported
	case strings.HasPrefix(message, "malformed document"):
		return CodeMalformed
	case strings.HasPrefix(message, "document is encrypted"):
		return CodeEncrypted
	case strings.HasPrefix(message, "resource limit exceeded"):
		return CodeResourceLimit
	case strings.HasPrefix(message, "missing required part:"):
		return CodeMissingPart
	case strings.HasPrefix(message, "io error:"):
		return CodeIO
	default:
		return CodeUnknown
	}
}
