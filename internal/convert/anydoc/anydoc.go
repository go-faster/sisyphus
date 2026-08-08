// Package anydoc converts office documents to GitHub-Flavored Markdown by
// running the anydoc CLI (github.com/firecrawl/anydoc) as a subprocess.
//
// Out of process on purpose; see the root CLAUDE.md for why.
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

// DefaultBinary is the executable looked up on PATH when [Options.Binary] is empty.
const DefaultBinary = "anydoc"

const (
	defaultTimeout        = 30 * time.Second
	defaultMaxOutputBytes = 8 << 20
	maxStderrBytes        = 8 << 10
)

var extensions = map[string]struct{}{
	".doc": {}, ".docx": {}, ".docm": {},
	".ppt": {}, ".pps": {}, ".pot": {}, ".pptx": {}, ".pptm": {}, ".ppsx": {}, ".ppsm": {},
	".xls": {}, ".xlsx": {}, ".xlsm": {}, ".xlsb": {},
	".odt": {}, ".ods": {}, ".odp": {},
	".rtf": {}, ".epub": {}, ".pdf": {},
}

// Options configures a [Converter].
type Options struct {
	// Binary is the anydoc executable, resolved through PATH when it is a bare name.
	Binary string
	// Timeout bounds a single conversion.
	Timeout time.Duration
	// MaxOutputBytes caps the Markdown one conversion may produce.
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

// New resolves the anydoc binary and returns a Converter for it. Resolution
// failure is reported by [Converter.Available], not here, so the caller decides
// whether it is fatal.
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

// Available reports whether the binary was resolved.
func (c *Converter) Available() error { return c.lookupErr }

// Supports reports whether ext, an extension with its leading dot, names a
// format anydoc converts.
func (c *Converter) Supports(ext string) bool {
	_, ok := extensions[strings.ToLower(ext)]
	return ok
}

// Convert returns the Markdown for the document at path. Every per-document
// failure is an [*Error]; only a Converter that cannot run returns anything else.
func (c *Converter) Convert(ctx context.Context, path string) (string, error) {
	if c.lookupErr != nil {
		return "", c.lookupErr
	}

	// Absolute so the path cannot be read as a flag: the CLI takes any
	// non-flag argument as its input file.
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
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	switch {
	case stdout.exceeded:
		return "", &Error{Code: CodeTooLarge, Message: "markdown exceeds the output cap"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", &Error{Code: CodeTimeout, Message: "conversion timed out after " + c.timeout.String()}
	case ctx.Err() != nil:
		// The caller's cancellation is not this document's fault.
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

// convertError classifies a non-zero exit. anydoc prints "error: <message>" for
// every input it refuses, so an exit saying anything else is the binary
// misbehaving rather than the document being unreadable.
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

// errorLine finds the "error: ..." line, which is not necessarily the last one:
// the CLI logs progress to stderr too.
func errorLine(stderr string) (string, bool) {
	for line := range strings.SplitSeq(stderr, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "error: "); ok {
			return after, true
		}
	}
	return "", false
}

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
	// Full write reported despite the cap: the child is killed by the caller,
	// not by a broken pipe mid-document.
	return len(p), nil
}

func (w *limitWriter) String() string { return w.buf.String() }
func (w *limitWriter) Len() int       { return w.buf.Len() }
