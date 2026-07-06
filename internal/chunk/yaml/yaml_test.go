package yaml

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestChunker_Chunk(t *testing.T) {
	ctx := context.Background()

	t.Run("single k8s resource", func(t *testing.T) {
		body := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: prod
spec:
  replicas: 3
`
		doc := index.Document{
			ID:   index.NewID(),
			Body: body,
			Metadata: map[string]any{
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 1)
		require.Equal(t, "Deployment my-app (prod)", chunks[0].Title)
		require.Equal(t, index.ChunkManifest, chunks[0].Type)
		require.Equal(t, "Deployment", chunks[0].Metadata["kind"])
		require.Equal(t, "my-app", chunks[0].Metadata["name"])
		require.Equal(t, "prod", chunks[0].Metadata["namespace"])
		require.Equal(t, "apps/v1", chunks[0].Metadata["apiVersion"])
	})

	t.Run("multi-doc yaml stream", func(t *testing.T) {
		body := `apiVersion: v1
kind: Namespace
metadata:
  name: staging
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: staging
`
		doc := index.Document{
			ID:   index.NewID(),
			Body: body,
			Metadata: map[string]any{
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 2)
		require.Equal(t, "Namespace staging", chunks[0].Title)
		require.Equal(t, "Deployment web (staging)", chunks[1].Title)
	})

	t.Run("non-k8s yaml gets file-level chunk", func(t *testing.T) {
		body := `key: value
nested:
  foo: bar
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "config.yaml",
			Body:  body,
			Metadata: map[string]any{
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 1)
		require.Equal(t, "config.yaml", chunks[0].Title)
	})

	t.Run("skips CRD", func(t *testing.T) {
		body := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: foos.example.com
spec:
  group: example.com
`
		doc := index.Document{
			ID:   index.NewID(),
			Body: body,
			Metadata: map[string]any{
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Empty(t, chunks)
	})

	t.Run("skips helm-rendered yaml", func(t *testing.T) {
		body := `# Source: mychart/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
`
		doc := index.Document{
			ID:   index.NewID(),
			Body: body,
			Metadata: map[string]any{
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Empty(t, chunks)
	})

	t.Run("empty body", func(t *testing.T) {
		doc := index.Document{
			ID:   index.NewID(),
			Body: "",
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Empty(t, chunks)
	})
}
