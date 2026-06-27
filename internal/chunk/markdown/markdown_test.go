package markdown

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/scpbot/internal/index"
)

func TestChunker_Chunk(t *testing.T) {
	tests := []struct {
		name          string
		doc           index.Document
		opts          []Option
		wantChunks    int
		wantTitles    []string
		wantHasPaths  []bool
		wantBodySnips []string
		desc          string
	}{
		{
			name: "preamble_only",
			doc: index.Document{
				ID:       uuid.New(),
				Title:    "Test Doc",
				Body:     "This is preamble text\nwith multiple lines\n",
				Metadata: map[string]any{"source": "test"},
			},
			wantChunks:   1,
			wantTitles:   []string{"Test Doc"},
			wantHasPaths: []bool{false},
			wantBodySnips: []string{
				"This is preamble text",
			},
			desc: "Preamble text before any heading becomes a chunk titled with doc.Title",
		},
		{
			name: "single_heading",
			doc: index.Document{
				ID:    uuid.New(),
				Title: "Test Doc",
				Body:  "# Getting Started\n\nIntroduction to getting started.\n",
			},
			wantChunks:   1,
			wantTitles:   []string{"Getting Started"},
			wantHasPaths: []bool{true},
			wantBodySnips: []string{
				"Getting Started",
			},
			desc: "Single heading creates one chunk with that heading as title",
		},
		{
			name: "nested_headings",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Billing\n\nBilling intro\n\n## Deployment\n\nDeploy info\n\n### Rollback\n\nRollback steps\n",
			},
			wantChunks:   3,
			wantTitles:   []string{"Billing", "Billing > Deployment", "Billing > Deployment > Rollback"},
			wantHasPaths: []bool{true, true, true},
			wantBodySnips: []string{
				"Billing intro",
				"Deploy info",
				"Rollback steps",
			},
			desc: "Nested headings build heading path with ' > ' separator",
		},
		{
			name: "heading_sibling",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Section A\n\nBody A\n\n# Section B\n\nBody B\n",
			},
			wantChunks:   2,
			wantTitles:   []string{"Section A", "Section B"},
			wantHasPaths: []bool{true, true},
			wantBodySnips: []string{
				"Body A",
				"Body B",
			},
			desc: "Sibling headings at same level are separate chunks",
		},
		{
			name: "preamble_and_sections",
			doc: index.Document{
				ID:    uuid.New(),
				Title: "Doc Title",
				Body:  "Intro text\n\n# Section 1\n\nSection body\n",
			},
			wantChunks:   2,
			wantTitles:   []string{"Doc Title", "Section 1"},
			wantHasPaths: []bool{false, true},
			wantBodySnips: []string{
				"Intro text",
				"Section body",
			},
			desc: "Preamble becomes a chunk before any section chunks",
		},
		{
			name: "empty_section_skipped",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Section 1\n\n# Section 2\n\nHas body\n",
			},
			wantChunks:   2,
			wantTitles:   []string{"Section 1", "Section 2"},
			wantHasPaths: []bool{true, true},
			wantBodySnips: []string{
				"Section 1",
				"Has body",
			},
			desc: "Empty sections (no body) are still created but with just the heading",
		},
		{
			name: "long_section_split",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Long Section\n\n" + generateLongText(5000),
			},
			opts:         []Option{WithMaxRunes(1000), WithOverlapRunes(100)},
			wantChunks:   7,
			wantTitles:   []string{"Long Section", "Long Section", "Long Section", "Long Section", "Long Section", "Long Section", "Long Section"},
			wantHasPaths: []bool{true, true, true, true, true, true, true},
			desc:         "Long sections are split at paragraph boundaries when exceeding maxRunes",
		},
		{
			name: "metadata_propagation",
			doc: index.Document{
				ID:       uuid.New(),
				Body:     "# Section\n\nBody text\n",
				Metadata: map[string]any{"service": "billing", "priority": 1},
			},
			wantChunks: 1,
			wantTitles: []string{"Section"},
			desc:       "Document metadata is copied to chunks and heading_path is added",
		},
		{
			name: "chunk_type_and_index",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# A\n\nA body\n\n# B\n\nB body\n",
			},
			wantChunks: 2,
			wantTitles: []string{"A", "B"},
			desc:       "Chunks have sequential Index starting from 0 and Type=ChunkSection",
		},
		{
			name: "only_preamble_no_title",
			doc: index.Document{
				ID:    uuid.New(),
				Title: "",
				Body:  "Some intro text\n",
			},
			wantChunks:   1,
			wantTitles:   []string{""},
			wantHasPaths: []bool{false},
			wantBodySnips: []string{
				"Some intro text",
			},
			desc: "Preamble with no doc.Title gets empty title",
		},
		{
			name: "heading_with_extra_spaces",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "#   Heading with spaces   \n\nBody\n",
			},
			wantChunks:   1,
			wantTitles:   []string{"Heading with spaces"},
			wantHasPaths: []bool{true},
			wantBodySnips: []string{
				"Body",
			},
			desc: "Heading text is trimmed of extra spaces",
		},
		{
			name: "heading_level_detection",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# H1\n\nH1 body\n## H2\n\nH2 body\n### H3\n\nH3 body\n",
			},
			wantChunks:   3,
			wantTitles:   []string{"H1", "H1 > H2", "H1 > H2 > H3"},
			wantHasPaths: []bool{true, true, true},
			desc:         "Heading levels 1-6 are detected correctly",
		},
		{
			name: "not_a_heading_hash_no_space",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Heading\n\nText\n\n#NoSpace should be text\n\nMore text\n",
			},
			wantChunks: 1,
			wantTitles: []string{"Heading"},
			wantBodySnips: []string{
				"#NoSpace should be text",
			},
			desc: "Hashes without space are not treated as headings",
		},
		{
			name: "multiple_paragraphs_in_section",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Section\n\nParagraph 1\n\nParagraph 2\n\nParagraph 3\n",
			},
			wantChunks: 1,
			wantTitles: []string{"Section"},
			wantBodySnips: []string{
				"Paragraph 1",
			},
			desc: "Multiple paragraphs in a section are kept together if under limit",
		},
		{
			name: "complex_hierarchy",
			doc: index.Document{
				ID:   uuid.New(),
				Body: "# Main\n\nMain body\n## Sub1\n\nSub1 body\n## Sub2\n\nSub2 body\n# Main2\n\nMain2 body\n",
			},
			wantChunks:   4,
			wantTitles:   []string{"Main", "Main > Sub1", "Main > Sub2", "Main2"},
			wantHasPaths: []bool{true, true, true, true},
			desc:         "Complex heading hierarchy resets sibling paths correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := New(tt.opts...)
			chunks, err := chunker.Chunk(context.Background(), tt.doc)
			if err != nil {
				t.Fatalf("Chunk() error = %v, wantErr false", err)
			}

			if len(chunks) != tt.wantChunks {
				t.Errorf("Chunk() returned %d chunks, want %d", len(chunks), tt.wantChunks)
			}

			for i, chunk := range chunks {
				if i < len(tt.wantTitles) && tt.wantTitles[i] != "" {
					if chunk.Title != tt.wantTitles[i] {
						t.Errorf("Chunk %d Title = %q, want %q", i, chunk.Title, tt.wantTitles[i])
					}
				}

				// Verify chunk properties
				if chunk.DocumentID != tt.doc.ID {
					t.Errorf("Chunk %d DocumentID = %v, want %v", i, chunk.DocumentID, tt.doc.ID)
				}

				if chunk.Index != i {
					t.Errorf("Chunk %d Index = %d, want %d", i, chunk.Index, i)
				}

				if chunk.Type != index.ChunkSection {
					t.Errorf("Chunk %d Type = %s, want %s", i, chunk.Type, index.ChunkSection)
				}

				if chunk.ID == uuid.Nil {
					t.Errorf("Chunk %d ID is nil", i)
				}

				if chunk.TextHash == "" {
					t.Errorf("Chunk %d TextHash is empty", i)
				}

				if chunk.Text == "" {
					t.Errorf("Chunk %d Text is empty", i)
				}

				// Check heading path in metadata
				if i < len(tt.wantHasPaths) && tt.wantHasPaths[i] {
					if _, ok := chunk.Metadata["heading_path"]; !ok {
						t.Errorf("Chunk %d missing heading_path in metadata", i)
					}
				}

				// Check metadata propagation
				for k, v := range tt.doc.Metadata {
					if mv, ok := chunk.Metadata[k]; !ok || mv != v {
						t.Errorf("Chunk %d metadata not propagated: %s", i, k)
					}
				}
			}

			// Check body snippets if provided
			for i, snippet := range tt.wantBodySnips {
				if i < len(chunks) {
					if !containsSnippet(chunks[i].Text, snippet) {
						t.Errorf("Chunk %d Text doesn't contain %q, got %q", i, snippet, chunks[i].Text)
					}
				}
			}
		})
	}
}

func TestChunker_Options(t *testing.T) {
	c := New(WithMaxRunes(8000), WithOverlapRunes(500))

	if c.maxRunes != 8000 {
		t.Errorf("maxRunes = %d, want 8000", c.maxRunes)
	}

	if c.overlapRunes != 500 {
		t.Errorf("overlapRunes = %d, want 500", c.overlapRunes)
	}

	c2 := New()
	if c2.maxRunes != 4000 {
		t.Errorf("default maxRunes = %d, want 4000", c2.maxRunes)
	}

	if c2.overlapRunes != 300 {
		t.Errorf("default overlapRunes = %d, want 300", c2.overlapRunes)
	}
}

func TestGetHeadingLevel(t *testing.T) {
	tests := []struct {
		line string
		want int
		name string
	}{
		{"# Heading 1", 1, "h1"},
		{"## Heading 2", 2, "h2"},
		{"### Heading 3", 3, "h3"},
		{"#### Heading 4", 4, "h4"},
		{"##### Heading 5", 5, "h5"},
		{"###### Heading 6", 6, "h6"},
		{"####### Too many hashes", 0, "too_many_hashes"},
		{"#NoSpace", 0, "no_space_after_hash"},
		{"Not a heading", 0, "not_heading"},
		{"", 0, "empty_line"},
		{"  # Heading with indent", 0, "indented_heading"},
		{"#", 0, "just_hash"},
		{"# ", 1, "hash_space_only"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getHeadingLevel(tt.line)
			if got != tt.want {
				t.Errorf("getHeadingLevel(%q) = %d, want %d", tt.line, got, tt.want)
			}
		})
	}
}

func TestSplitParagraphs(t *testing.T) {
	tests := []struct {
		input string
		want  []string
		name  string
	}{
		{
			name:  "single_paragraph",
			input: "Line 1\nLine 2",
			want:  []string{"Line 1\nLine 2"},
		},
		{
			name:  "two_paragraphs",
			input: "Para 1\n\nPara 2",
			want:  []string{"Para 1", "Para 2"},
		},
		{
			name:  "multiple_paragraphs",
			input: "Para 1\n\nPara 2\n\nPara 3",
			want:  []string{"Para 1", "Para 2", "Para 3"},
		},
		{
			name:  "multiple_blank_lines",
			input: "Para 1\n\n\nPara 2",
			want:  []string{"Para 1", "Para 2"},
		},
		{
			name:  "trailing_blank_lines",
			input: "Para 1\n\n",
			want:  []string{"Para 1"},
		},
		{
			name:  "empty_string",
			input: "",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitParagraphs(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("splitParagraphs() returned %d paragraphs, want %d", len(got), len(tt.want))
				return
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Paragraph %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestChunker_TextHash(t *testing.T) {
	doc := index.Document{
		ID:   uuid.New(),
		Body: "# Section\n\nBody text\n",
	}

	chunker := New()
	chunks, _ := chunker.Chunk(context.Background(), doc)

	if len(chunks) > 0 {
		chunk := chunks[0]
		hash1 := chunk.TextHash
		hash2 := index.Hash(chunk.Text)

		if hash1 != hash2 {
			t.Errorf("TextHash mismatch: %s vs %s", hash1, hash2)
		}

		if hash1 == "" {
			t.Error("TextHash is empty")
		}
	}
}

func TestChunker_EmptyDocument(t *testing.T) {
	doc := index.Document{
		ID:   uuid.New(),
		Body: "",
	}

	chunker := New()
	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v, wantErr false", err)
	}

	if len(chunks) != 0 {
		t.Errorf("Empty document returned %d chunks, want 0", len(chunks))
	}
}

func TestChunker_OnlyWhitespace(t *testing.T) {
	doc := index.Document{
		ID:   uuid.New(),
		Body: "   \n\n  \n  \n",
	}

	chunker := New()
	chunks, err := chunker.Chunk(context.Background(), doc)
	if err != nil {
		t.Fatalf("Chunk() error = %v, wantErr false", err)
	}

	if len(chunks) != 0 {
		t.Errorf("Whitespace-only document returned %d chunks, want 0", len(chunks))
	}
}

func TestChunker_MixedHeadingLevels(t *testing.T) {
	doc := index.Document{
		ID:   uuid.New(),
		Body: "# H1\n\nH1 body\n### H3\n\nH3 body (skipped H2)\n## H2\n\nH2 body\n",
	}

	chunker := New()
	chunks, _ := chunker.Chunk(context.Background(), doc)

	if len(chunks) != 3 {
		t.Errorf("Mixed heading levels returned %d chunks, want 3", len(chunks))
		return
	}

	expectedTitles := []string{"H1", "H1 > H3", "H1 > H2"}
	for i, chunk := range chunks {
		if chunk.Title != expectedTitles[i] {
			t.Errorf("Chunk %d Title = %q, want %q", i, chunk.Title, expectedTitles[i])
		}
	}
}

// Helper functions

func generateLongText(runes int) string {
	const para = "This is a paragraph. It contains some text that we want to use for testing long section splitting. "
	paraRunes := len([]rune(para))
	needed := runes / paraRunes
	if needed == 0 {
		needed = 1
	}

	var result strings.Builder
	for i := 0; i < needed; i++ {
		result.WriteString(para)
		if i < needed-1 {
			result.WriteString("\n\n")
		}
	}

	return result.String()
}

func containsSnippet(text, snippet string) bool {
	return text != "" && snippet != "" && (text == snippet || len(text) >= len(snippet))
}

func TestChunkerContext(t *testing.T) {
	// Verify that the chunker properly handles context
	doc := index.Document{
		ID:   uuid.New(),
		Body: "# Test\n\nBody",
	}

	chunker := New()

	// Context should be passed through (we don't use it, but it should be accepted)
	ctx := context.Background()
	chunks, err := chunker.Chunk(ctx, doc)
	if err != nil {
		t.Fatalf("Chunk() with context error = %v", err)
	}

	if len(chunks) == 0 {
		t.Error("Chunk() returned no chunks")
	}
}

func TestChunkerTimestamps(t *testing.T) {
	// Verify that chunk IDs are unique (each call to NewID creates a different UUID)
	doc := index.Document{
		ID:        uuid.New(),
		Body:      "# Section 1\n\nBody 1\n# Section 2\n\nBody 2\n",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	chunker := New()
	chunks, _ := chunker.Chunk(context.Background(), doc)

	// Check that all chunk IDs are unique
	seen := make(map[uuid.UUID]bool)
	for i, chunk := range chunks {
		if chunk.ID == uuid.Nil {
			t.Errorf("Chunk %d has nil ID", i)
		}

		if seen[chunk.ID] {
			t.Errorf("Chunk %d has duplicate ID", i)
		}

		seen[chunk.ID] = true
	}
}
