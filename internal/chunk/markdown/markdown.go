// Package markdown implements a heading-aware Markdown chunker.
package markdown

import (
	"context"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/go-faster/sisyphus/internal/index"
)

// Chunker splits a Markdown document into sections by ATX headings.
type Chunker struct {
	maxRunes     int
	overlapRunes int
}

// ChunkerOptions configures a Chunker.
type ChunkerOptions struct {
	// MaxRunes is the maximum rune budget for a section before splitting.
	// Default is ~4000 runes.
	MaxRunes int
	// OverlapRunes is the overlap runes when splitting long sections.
	// Default is ~300 runes.
	OverlapRunes int
}

func (opts *ChunkerOptions) setDefaults() {
	if opts.MaxRunes == 0 {
		opts.MaxRunes = 4000
	}
	if opts.OverlapRunes == 0 {
		opts.OverlapRunes = 300
	}
}

// New creates a new Markdown chunker.
func New(opts ChunkerOptions) *Chunker {
	opts.setDefaults()
	return &Chunker{
		maxRunes:     opts.MaxRunes,
		overlapRunes: opts.OverlapRunes,
	}
}

// Chunk implements index.Chunker.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	lines := strings.Split(doc.Body, "\n")

	// First, identify all headings and their positions
	type lineInfo struct {
		index int
		level int
		text  string
	}

	var headingLines []lineInfo
	var preambleLines []int

	for i, line := range lines {
		level := getHeadingLevel(line)
		if level > 0 {
			headingText := strings.TrimSpace(strings.TrimLeft(line, "#"))
			headingLines = append(headingLines, lineInfo{index: i, level: level, text: headingText})
		} else if len(headingLines) == 0 {
			// Before first heading
			preambleLines = append(preambleLines, i)
		}
	}

	var chunks []index.Chunk
	chunkIndex := 0

	// Preamble chunk
	if len(preambleLines) > 0 {
		var preambleBody []string
		for _, i := range preambleLines {
			preambleBody = append(preambleBody, lines[i])
		}
		preambleText := strings.Join(preambleBody, "\n")
		preambleText = strings.TrimSpace(preambleText)

		if preambleText != "" {
			chunk := c.createChunk(doc, doc.Title, []string{}, preambleText, chunkIndex)
			chunks = append(chunks, chunk)
			chunkIndex++
		}
	}

	// Process sections
	var headingPath []headingLevel

	for i, headingLine := range headingLines {
		// Determine section body: from current heading to start of next heading
		sectionStart := headingLine.index + 1
		sectionEnd := len(lines)
		if i+1 < len(headingLines) {
			sectionEnd = headingLines[i+1].index
		}

		// Trim the path to ancestors
		for len(headingPath) > 0 && headingPath[len(headingPath)-1].level >= headingLine.level {
			headingPath = headingPath[:len(headingPath)-1]
		}

		// Add current heading
		headingPath = append(headingPath, headingLevel{
			level: headingLine.level,
			text:  headingLine.text,
		})

		// Extract body lines
		var bodyLines []string
		for j := sectionStart; j < sectionEnd; j++ {
			bodyLines = append(bodyLines, lines[j])
		}

		bodyText := strings.Join(bodyLines, "\n")
		bodyText = strings.TrimSpace(bodyText)

		// Create path strings
		path := make([]string, len(headingPath))
		for j, h := range headingPath {
			path[j] = h.text
		}

		title := strings.Join(path, " > ")

		// Check if we need to split this section
		sectionChunks := c.splitLongSection(doc, title, path, bodyText, chunkIndex)
		chunks = append(chunks, sectionChunks...)
		chunkIndex += len(sectionChunks)
	}

	return chunks, nil
}

// headingLevel represents a heading and its level.
type headingLevel struct {
	level int
	text  string
}

// getHeadingLevel returns the heading level (1-6) of a line, or 0 if not a heading.
func getHeadingLevel(line string) int {
	trimmed := strings.TrimLeft(line, "#")
	if len(trimmed) == len(line) || trimmed == "" {
		return 0 // Not a heading
	}
	// Must have a space after the hashes
	if trimmed[0] != ' ' && trimmed[0] != '\t' {
		return 0
	}
	level := len(line) - len(trimmed)
	if level > 6 {
		return 0
	}
	return level
}

// createChunk creates a single chunk.
func (c *Chunker) createChunk(doc index.Document, title string, headingPath []string, bodyText string, chunkIdx int) index.Chunk {
	text := bodyText
	if len(headingPath) > 0 {
		headingLine := strings.Join(headingPath, " > ")
		text = headingLine + "\n" + bodyText
	}

	metadata := make(map[string]any)
	maps.Copy(metadata, doc.Metadata)
	metadata["heading_path"] = headingPath

	return index.Chunk{
		ID:         index.NewID(),
		DocumentID: doc.ID,
		Index:      chunkIdx,
		Type:       index.ChunkSection,
		Title:      title,
		Text:       text,
		TextHash:   index.Hash(text),
		Metadata:   metadata,
	}
}

// splitLongSection splits a long section into multiple chunks at paragraph boundaries.
func (c *Chunker) splitLongSection(doc index.Document, title string, headingPath []string, bodyText string, startIndex int) []index.Chunk {
	bodyRunes := utf8.RuneCountInString(bodyText)

	if bodyRunes <= c.maxRunes {
		// No split needed
		return []index.Chunk{c.createChunk(doc, title, headingPath, bodyText, startIndex)}
	}

	// Split at paragraph boundaries
	paragraphs := splitParagraphs(bodyText)
	if len(paragraphs) == 0 {
		return []index.Chunk{c.createChunk(doc, title, headingPath, bodyText, startIndex)}
	}

	var (
		chunks       []index.Chunk
		currentChunk []string
		currentIndex = startIndex
	)

	for _, para := range paragraphs {
		paraRunes := utf8.RuneCountInString(para)

		// Calculate current chunk size (paragraphs joined with \n\n)
		var currentRunes int
		if len(currentChunk) > 0 {
			currentText := strings.Join(currentChunk, "\n\n")
			currentRunes = utf8.RuneCountInString(currentText) + 2 // +2 for the \n\n separator before new para
		}

		if currentRunes+paraRunes <= c.maxRunes || len(currentChunk) == 0 {
			// Add to current chunk (including first para or if it fits)
			currentChunk = append(currentChunk, para)
		} else {
			// Flush current chunk
			chunkBody := strings.Join(currentChunk, "\n\n")
			chunks = append(chunks, c.createChunk(doc, title, headingPath, chunkBody, currentIndex))
			currentIndex++

			// Start new chunk with last paragraph as overlap
			lastPara := currentChunk[len(currentChunk)-1]
			currentChunk = []string{lastPara, para}
		}
	}

	// Flush last chunk
	if len(currentChunk) > 0 {
		chunkBody := strings.Join(currentChunk, "\n\n")
		chunks = append(chunks, c.createChunk(doc, title, headingPath, chunkBody, currentIndex))
	}

	return chunks
}

// splitParagraphs splits text into paragraphs (separated by blank lines).
func splitParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var (
		paragraphs   []string
		currentLines []string
	)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(currentLines) > 0 {
				paragraphs = append(paragraphs, strings.Join(currentLines, "\n"))
				currentLines = nil
			}
		} else {
			currentLines = append(currentLines, line)
		}
	}
	if len(currentLines) > 0 {
		paragraphs = append(paragraphs, strings.Join(currentLines, "\n"))
	}
	return paragraphs
}
