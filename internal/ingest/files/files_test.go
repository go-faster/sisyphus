package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestWalk(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\n\nBody"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "tmp"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tmp", "skip.md"), []byte("# Skip"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0xff}, 0o600))

	docs, err := Walk(t.Context(), []Source{{
		Name:    "runbooks",
		Root:    root,
		BaseURL: "https://example.test/docs",
		Include: []string{"**/*.md"},
		Exclude: []string{"tmp/**"},
	}})
	require.NoError(t, err)
	require.Len(t, docs, 1)

	doc := docs[0]
	require.Equal(t, index.SourceContextFiles("runbooks"), doc.Source)
	require.Equal(t, "runbooks:guide.md", doc.SourceID)
	require.Equal(t, "Guide", doc.Title)
	require.Equal(t, "https://example.test/docs/guide.md", doc.URL)
	require.Equal(t, string(index.AuthorityHigh), doc.Metadata["authority"])
	require.Equal(t, "markdown", doc.Metadata["lang"])
}

func TestWalk_RequiresSourceFields(t *testing.T) {
	_, err := Walk(t.Context(), []Source{{Root: t.TempDir()}})
	require.Error(t, err)

	_, err = Walk(t.Context(), []Source{{Name: "runbooks"}})
	require.Error(t, err)
}
