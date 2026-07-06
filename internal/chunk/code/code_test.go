package code

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestChunker_Chunk(t *testing.T) {
	ctx := context.Background()

	t.Run("Go file overview and symbols", func(t *testing.T) {
		body := `package main

import (
	"fmt"
)

// hello greets the user.
func hello(name string) string {
	return "Hello, " + name
}

type Person struct {
	Name string
}

const defaultName = "world"
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "main.go",
			Body:  body,
			Metadata: map[string]any{
				"lang": "go",
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.NotEmpty(t, chunks)

		// First chunk should be the file overview
		overview := chunks[0]
		require.Equal(t, index.ChunkCodeFile, overview.Type)
		require.Contains(t, overview.Title, "main")
		require.Contains(t, overview.Text, "package main")
		require.Contains(t, overview.Text, "hello(name string) string")
		require.Contains(t, overview.Text, "type Person")
		require.Contains(t, overview.Text, "defaultName")

		// We should have overview + 3 symbols (hello, Person, defaultName)
		require.Len(t, chunks, 4)
		for i, chunk := range chunks {
			require.Equal(t, i, chunk.Index)
		}

		// The hello symbol chunk must include its doc comment.
		hello := chunks[1]
		require.Equal(t, index.ChunkCodeSymbol, hello.Type)
		require.Equal(t, "hello", hello.Title)
		require.Contains(t, hello.Text, "// hello greets the user.")
		require.Contains(t, hello.Text, "func hello(name string) string")
	})

	t.Run("Go methods have receiver metadata", func(t *testing.T) {
		body := `package service

type UserService struct{}

func (s *UserService) GetUser(id int) string {
	return "user"
}
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "service.go",
			Body:  body,
			Metadata: map[string]any{
				"lang": "go",
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 3) // overview + UserService type + GetUser method

		method := chunks[2]
		require.Equal(t, index.ChunkCodeSymbol, method.Type)
		require.Equal(t, "(*UserService).GetUser", method.Title)
		require.Equal(t, "func", method.Metadata["symbol_kind"])
		require.Equal(t, "*UserService", method.Metadata["receiver"])
	})

	t.Run("non-Go files use generic splitter", func(t *testing.T) {
		body := `function greet(name: string): string {
	return "Hello, " + name;
}

interface Person {
	name: string;
	age: number;
}
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "greeter.ts",
			Body:  body,
			Metadata: map[string]any{
				"lang": "typescript",
				"repo": "test/repo",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 2)
		require.Contains(t, chunks[0].Text, "function greet")
		require.Contains(t, chunks[1].Text, "interface Person")
	})

	t.Run("non-Go long block is windowed", func(t *testing.T) {
		body := strings.Repeat("a", 25)
		doc := index.Document{
			ID:    index.NewID(),
			Title: "long.ts",
			Body:  body,
			Metadata: map[string]any{
				"lang": "typescript",
			},
		}

		c := New(ChunkerOptions{MaxWindowRunes: 10, OverlapRunes: 2})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 3)
		for i, chunk := range chunks {
			require.Equal(t, i, chunk.Index)
			require.LessOrEqual(t, len([]rune(chunk.Text)), 10)
		}
		require.Equal(t, strings.Repeat("a", 10), chunks[0].Text)
		require.Equal(t, strings.Repeat("a", 10), chunks[1].Text)
		require.Equal(t, strings.Repeat("a", 9), chunks[2].Text)
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

	t.Run("generic title truncates by runes", func(t *testing.T) {
		body := `/** Проверка на enabled, т.к. по дефолту скипаются 0 */
export const apiCustomEnabledCheck = () => true;
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "query-options-mutator.ts",
			Body:  body,
			Metadata: map[string]any{
				"lang": "typescript",
			},
		}

		c := New(ChunkerOptions{})
		chunks, err := c.Chunk(ctx, doc)
		require.NoError(t, err)
		require.Len(t, chunks, 1)
		require.True(t, utf8.ValidString(chunks[0].Title))
		require.Equal(t, "/** Проверка на enabled, т.к. по дефолту скипаются 0 */", chunks[0].Title)
	})

	t.Run("deterministic hashing", func(t *testing.T) {
		body := `package main

func foo() {}

func bar() {}
`
		doc := index.Document{
			ID:    index.NewID(),
			Title: "main.go",
			Body:  body,
			Metadata: map[string]any{
				"lang": "go",
			},
		}

		c := New(ChunkerOptions{})
		chunks1, err := c.Chunk(ctx, doc)
		require.NoError(t, err)

		chunks2, err := c.Chunk(ctx, doc)
		require.NoError(t, err)

		require.Equal(t, len(chunks1), len(chunks2))
		for i := range chunks1 {
			require.Equal(t, chunks1[i].TextHash, chunks2[i].TextHash)
		}
	})
}
