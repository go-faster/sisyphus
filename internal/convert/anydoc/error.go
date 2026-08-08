package anydoc

import "strings"

// Codes a conversion can fail with. The first six mirror anydoc's own
// ConvertError variants, which it documents as stable; the rest name failures of
// the subprocess itself.
const (
	CodeUnsupported   = "unsupported"
	CodeMalformed     = "malformed"
	CodeEncrypted     = "encrypted"
	CodeResourceLimit = "resourceLimit"
	CodeMissingPart   = "missingPart"
	CodeIO            = "io"
	CodeTimeout       = "timeout"
	CodeTooLarge      = "tooLarge"
	CodeCrashed       = "crashed"
	CodeUnknown       = "unknown"
)

// Error is one document anydoc could not turn into Markdown. It is always
// per-document: the caller counts it, skips the file and keeps walking.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return "convert document (" + e.Code + "): " + e.Message }

// classify maps an anydoc error message onto its Code. The CLI prints
// ConvertError's Display text rather than its code(), so the prefix is what
// there is to match on; an unrecognized message still skips the document, so a
// wrong guess costs only a log label.
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
