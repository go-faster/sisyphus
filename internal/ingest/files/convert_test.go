package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

// stubConverter converts the supported extensions to a fixed body, and fails
// for any path under a "broken" name.
type stubConverter struct {
	calls []string
}

func (c *stubConverter) Supports(ext string) bool {
	return ext == ".docx" || ext == ".pdf"
}

func (c *stubConverter) Convert(_ context.Context, path string) (string, error) {
	c.calls = append(c.calls, filepath.Base(path))
	if strings.Contains(filepath.Base(path), "broken") {
		return "", errors.New("malformed document")
	}
	return "# Converted\n\nBody from " + filepath.Base(path), nil
}

func TestWalk_Converts(t *testing.T) {
	root := t.TempDir()
	// Not valid UTF-8, so the pre-converter walk skipped both of these.
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.docx"), []byte{0x50, 0x4b, 0xff}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\n\nBody"), 0o600))

	conv := &stubConverter{}
	docs, err := Walk(t.Context(), []Source{{
		Name: "runbooks",
		Root: root,
	}}, Options{Converter: conv})
	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.Equal(t, []string{"report.docx"}, conv.calls)

	byID := map[string]index.Document{}
	for _, d := range docs {
		byID[d.SourceID] = d
	}

	converted := byID["runbooks:report.docx"]
	require.Equal(t, "Converted", converted.Title, "title comes from the converted Markdown")
	require.Contains(t, converted.Body, "Body from report.docx")
	require.Equal(t, index.Hash(converted.Body), converted.BodyHash)
	require.Equal(t, "markdown", converted.Metadata["lang"], "the body is Markdown now, whatever the file is")
	require.Equal(t, "docx", converted.Metadata["converted_from"])

	// An ordinary text file is untouched by the converter.
	plain := byID["runbooks:guide.md"]
	require.Equal(t, "markdown", plain.Metadata["lang"])
	require.NotContains(t, plain.Metadata, "converted_from")
}

// A document the converter refuses is one document lost, not the walk: an
// encrypted file in a docs root must not hide every file after it.
func TestWalk_ConversionFailureSkipsOnlyThatDocument(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a-broken.docx"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b-fine.docx"), []byte("x"), 0o600))

	docs, err := Walk(t.Context(), []Source{{Name: "runbooks", Root: root}}, Options{Converter: &stubConverter{}})
	require.NoError(t, err)
	require.Len(t, docs, 1)
	require.Equal(t, "runbooks:b-fine.docx", docs[0].SourceID)
}

// Without a converter the walk behaves exactly as it did before one existed.
func TestWalk_NoConverterSkipsBinary(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.docx"), []byte{0x50, 0x4b, 0xff}, 0o600))

	docs, err := Walk(t.Context(), []Source{{Name: "runbooks", Root: root}}, Options{})
	require.NoError(t, err)
	require.Empty(t, docs)
}

// Include/Exclude decide before the converter does, so a converter cannot
// widen a source's configured file set.
func TestWalk_ConverterRespectsPatterns(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.docx"), []byte("x"), 0o600))

	conv := &stubConverter{}
	docs, err := Walk(t.Context(), []Source{{
		Name:    "runbooks",
		Root:    root,
		Include: []string{"**/*.md"},
	}}, Options{Converter: conv})
	require.NoError(t, err)
	require.Empty(t, docs)
	require.Empty(t, conv.calls, "an excluded file is never converted")
}
