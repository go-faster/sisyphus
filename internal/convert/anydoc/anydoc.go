// Package anydoc converts office documents (Word, PowerPoint, Excel,
// OpenDocument, RTF, EPUB, PDF) to GitHub-Flavored Markdown by running the
// anydoc CLI as a subprocess.
//
// Out of process on purpose. The converter parses untrusted documents from
// wherever a context-file root points, and the failure modes of a document
// parser are the ones a library cannot contain: an unbounded allocation, a
// parse that does not terminate, a crash. A child process bounds all three
// with a deadline, an output cap and its own address space, so a hostile file
// costs one document instead of the ingest run. It also keeps every binary
// CGO_ENABLED=0.
//
// The child is anydoc's own CLI (github.com/firecrawl/anydoc): Markdown on
// stdout, progress and errors on stderr, exit 0 on success.
package anydoc

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"
)

// DefaultBinary is the executable looked up on PATH when [Options.Binary] is
// empty.
const DefaultBinary = "anydoc"

const (
	defaultTimeout        = 30 * time.Second
	defaultMaxOutputBytes = 8 << 20
	maxStderrBytes        = 8 << 10
)

// extensions is every extension anydoc documents a parser for, lowercase and
// dotted - minus .csv.
//
// Conversion is gated on the extension rather than on anydoc's own
// content-based detection, because the walk has to decide whether to convert
// before it reads the file. A mislabeled document is therefore skipped as
// before, not converted.
//
// CSV is left out although anydoc converts it: it is the one supported format
// that is already valid UTF-8 text, so it is the one where enabling the
// converter would *change* how documents already in the index are indexed,
// re-embedding every one of them. Converting the formats that were previously
// invisible costs nothing that was working before.
var extensions = map[string]struct{}{
	".doc": {}, ".docx": {}, ".docm": {},
	".ppt": {}, ".pps": {}, ".pot": {}, ".pptx": {}, ".pptm": {}, ".ppsx": {}, ".ppsm": {},
	".xls": {}, ".xlsx": {}, ".xlsm": {}, ".xlsb": {},
	".odt": {}, ".ods": {}, ".odp": {},
	".rtf": {}, ".epub": {}, ".pdf": {},
}

// Options configures a [Converter].
type Options struct {
	// Binary is the anydoc executable. A bare name is resolved through PATH.
	// Defaults to DefaultBinary.
	Binary string
	// Timeout bounds a single conversion. Defaults to 30s.
	Timeout time.Duration
	// MaxOutputBytes caps the Markdown one conversion may produce. Defaults
	// to 8 MiB.
	MaxOutputBytes int64
	Logger         *zap.Logger
}

func (opts *Options) setDefaults() {
	if opts.Binary == "" {
		opts.Binary = DefaultBinary
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = defaultMaxOutputBytes
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}

// Converter runs the anydoc CLI.
type Converter struct {
	binary    string
	lookupErr error
	timeout   time.Duration
	maxOutput int64
	lg        *zap.Logger
}

// New resolves the anydoc binary and returns a Converter for it. It never
// fails: a binary that could not be resolved is reported by [Converter.Available]
// so the caller can decide whether that is fatal.
func New(opts Options) *Converter {
	opts.setDefaults()

	binary, err := exec.LookPath(opts.Binary)
	if err != nil {
		err = errors.Wrapf(err, "resolve anydoc binary %q", opts.Binary)
	}
	return &Converter{
		binary:    binary,
		lookupErr: err,
		timeout:   opts.Timeout,
		maxOutput: opts.MaxOutputBytes,
		lg:        opts.Logger,
	}
}

// Available reports whether the binary was resolved. When it returns an
// error the Converter converts nothing.
func (c *Converter) Available() error { return c.lookupErr }

// Supports reports whether ext names a format anydoc converts. ext is an
// extension with its leading dot, matched case-insensitively.
func (c *Converter) Supports(ext string) bool {
	_, ok := extensions[strings.ToLower(ext)]
	return ok
}

// Convert returns the Markdown for the document at path.
//
// Every per-document failure - a refusal from anydoc, a timeout, a child that
// died - is reported as [*Error], which the caller is expected to count and
// skip. Only a Converter that cannot run at all returns anything else.
func (c *Converter) Convert(ctx context.Context, path string) (string, error) {
	if c.lookupErr != nil {
		return "", c.lookupErr
	}

	// Absolute, so the path can never be read as one of the CLI's flags: its
	// argument parser takes any non-flag argument as the input file.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.Wrap(err, "absolute document path")
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	stdout := &limitWriter{limit: c.maxOutput}
	stderr := &limitWriter{limit: maxStderrBytes}

	//nolint:gosec // binary is operator config, abs comes from the fs walk; neither is user input
	cmd := exec.CommandContext(ctx, c.binary, abs)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// A child that outlives the kill signal must not hold Wait open.
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	switch {
	case stdout.exceeded:
		return "", &Error{Code: CodeTooLarge, Message: "markdown exceeds the output cap"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", &Error{Code: CodeTimeout, Message: "conversion timed out after " + c.timeout.String()}
	case ctx.Err() != nil:
		// The caller's context went away, which is not this document's fault.
		return "", ctx.Err()
	case runErr != nil:
		return "", convertError(runErr, stderr.String())
	}

	c.lg.Debug("converted document",
		zap.String("path", abs),
		zap.Int("markdown_bytes", stdout.Len()),
	)
	return stdout.String(), nil
}

// convertError classifies a non-zero exit. anydoc prints "error: <message>"
// for every input it refuses, so an exit that says anything else is the
// binary misbehaving rather than the document being unreadable - but both are
// per-document, so both are an *Error.
func convertError(runErr error, stderr string) error {
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return &Error{Code: CodeCrashed, Message: runErr.Error()}
	}

	message, ok := errorLine(stderr)
	if !ok {
		return &Error{
			Code:    CodeCrashed,
			Message: strings.TrimSpace(exitErr.String() + ": " + stderr),
		}
	}
	return &Error{Code: classify(message), Message: message}
}

// errorLine finds the "error: ..." line anydoc prints for a refused input.
// It is not necessarily the last line: the CLI also logs progress to stderr.
func errorLine(stderr string) (string, bool) {
	for line := range strings.SplitSeq(stderr, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "error: "); ok {
			return after, true
		}
	}
	return "", false
}

// limitWriter collects output up to limit bytes and records having been
// asked for more, so a runaway conversion cannot be buffered into the
// ingester's heap.
type limitWriter struct {
	buf      bytes.Buffer
	limit    int64
	exceeded bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.limit - int64(w.buf.Len()); room > 0 {
		if int64(len(p)) > room {
			w.exceeded = true
			p = p[:room]
		}
		w.buf.Write(p)
	} else if len(p) > 0 {
		w.exceeded = true
	}
	// Report the full write: the child is killed by the caller, not by a
	// broken pipe mid-document.
	return len(p), nil
}

func (w *limitWriter) String() string { return w.buf.String() }
func (w *limitWriter) Len() int       { return w.buf.Len() }
