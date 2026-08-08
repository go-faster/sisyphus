package anydoc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
)

// The test binary re-execs itself as the anydoc CLI, so it has to tell a stub
// invocation from an ordinary `go test` one. The input path is the only argument
// a conversion passes, so the fixture name carries the directive.
const stubMarker = "anydocstub"

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && strings.Contains(filepath.Base(os.Args[1]), stubMarker) {
		os.Exit(stubMain(os.Args[1]))
	}
	os.Exit(m.Run())
}

// stubMain stands in for the anydoc CLI: Markdown on stdout, progress and
// errors on stderr, exit 0 on success.
func stubMain(path string) int {
	name := filepath.Base(path)
	fmt.Fprintf(os.Stderr, "converted %s in 0.42ms\n", path)

	switch {
	case strings.Contains(name, "encrypted"):
		fmt.Fprintln(os.Stderr, "error: document is encrypted")
		return 1
	case strings.Contains(name, "unsupported"):
		fmt.Fprintln(os.Stderr, "error: unsupported input: unrecognized file content and extension")
		return 1
	case strings.Contains(name, "silent"):
		return 3
	case strings.Contains(name, "hang"):
		time.Sleep(30 * time.Second)
		return 0
	case strings.Contains(name, "huge"):
		fmt.Print(strings.Repeat("x", 4096))
		return 0
	default:
		fmt.Print("# Converted\n\nBody.\n")
		return 0
	}
}

func newStubConverter(t *testing.T, opts Options) *Converter {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	opts.Binary = exe

	conv, err := New(opts)
	require.NoError(t, err)
	require.NoError(t, conv.Available())
	return conv
}

func stubPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), stubMarker+"-"+name+".docx")
	require.NoError(t, os.WriteFile(path, []byte("fixture"), 0o600))
	return path
}

func TestConverter_Convert(t *testing.T) {
	conv := newStubConverter(t, Options{})

	md, err := conv.Convert(t.Context(), stubPath(t, "ok"))
	require.NoError(t, err)
	require.Equal(t, "# Converted\n\nBody.\n", md, "stderr progress never reaches the Markdown")
}

func TestConverter_ConvertErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fixture string
		code    string
	}{
		{"encrypted", "encrypted", CodeEncrypted},
		{"unsupported", "unsupported", CodeUnsupported},
		{"no error line", "silent", CodeCrashed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conv := newStubConverter(t, Options{})

			_, err := conv.Convert(t.Context(), stubPath(t, tt.fixture))
			require.Error(t, err)

			var convErr *Error
			require.True(t, errors.As(err, &convErr), "every per-document failure is an *Error")
			require.Equal(t, tt.code, convErr.Code)
		})
	}
}

func TestConverter_Timeout(t *testing.T) {
	conv := newStubConverter(t, Options{Timeout: 200 * time.Millisecond})

	start := time.Now()
	_, err := conv.Convert(t.Context(), stubPath(t, "hang"))
	require.Error(t, err)
	require.Less(t, time.Since(start), 25*time.Second, "the child is killed, not waited on")

	var convErr *Error
	require.True(t, errors.As(err, &convErr))
	require.Equal(t, CodeTimeout, convErr.Code)
}

func TestConverter_OutputCap(t *testing.T) {
	conv := newStubConverter(t, Options{MaxOutputBytes: 64})

	_, err := conv.Convert(t.Context(), stubPath(t, "huge"))
	require.Error(t, err)

	var convErr *Error
	require.True(t, errors.As(err, &convErr))
	require.Equal(t, CodeTooLarge, convErr.Code)
}

func TestConverter_CallerCancellation(t *testing.T) {
	conv := newStubConverter(t, Options{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := conv.Convert(ctx, stubPath(t, "ok"))
	require.ErrorIs(t, err, context.Canceled)

	var convErr *Error
	require.False(t, errors.As(err, &convErr))
}

func TestConverter_Unavailable(t *testing.T) {
	conv, err := New(Options{Binary: "sisyphus-no-such-binary"})
	require.NoError(t, err)
	require.Error(t, conv.Available())

	_, err = conv.Convert(t.Context(), "doc.docx")
	require.Error(t, err)

	var convErr *Error
	require.False(t, errors.As(err, &convErr), "a converter that cannot run is not a per-document failure")
}

func TestConverter_Supports(t *testing.T) {
	conv, err := New(Options{})
	require.NoError(t, err)
	for _, tt := range []struct {
		ext  string
		want bool
	}{
		{".docx", true},
		{".DOCX", true},
		{".pdf", true},
		{".xlsb", true},
		{".csv", false},
		{".md", false},
		{".txt", false},
		{"", false},
		{"docx", false},
	} {
		require.Equalf(t, tt.want, conv.Supports(tt.ext), "Supports(%q)", tt.ext)
	}
}

func TestClassify(t *testing.T) {
	for _, tt := range []struct {
		message string
		want    string
	}{
		{"unsupported input: unrecognized file content and extension: a.bin", CodeUnsupported},
		{"malformed document: not a readable zip archive: invalid Zip archive", CodeMalformed},
		{"malformed document (word/document.xml): unexpected end", CodeMalformed},
		{"document is encrypted", CodeEncrypted},
		{"resource limit exceeded (max_entry_bytes): word/document.xml decompresses to too much", CodeResourceLimit},
		{"missing required part: word/document.xml", CodeMissingPart},
		{"io error: permission denied", CodeIO},
		{"something anydoc has not said before", CodeUnknown},
	} {
		require.Equalf(t, tt.want, classify(tt.message), "classify(%q)", tt.message)
	}
}

func TestErrorLine(t *testing.T) {
	message, ok := errorLine("converted a.docx in 0.42ms\nerror: document is encrypted\n")
	require.True(t, ok)
	require.Equal(t, "document is encrypted", message)

	_, ok = errorLine("converted a.docx in 0.42ms\n")
	require.False(t, ok)
}
