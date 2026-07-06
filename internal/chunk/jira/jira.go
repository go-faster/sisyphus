// Package jira implements a Jira issue chunker.
package jira

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// Comment represents a single comment on a Jira issue.
type Comment struct {
	Author  string
	Body    string
	Created time.Time
}

// Issue models a Jira issue.
type Issue struct {
	Key         string
	Title       string // summary
	Description string
	Status      string
	Resolution  string
	Components  []string
	Labels      []string
	Assignee    string
	Reporter    string
	Created     time.Time
	Updated     time.Time
	Resolved    time.Time
	Comments    []Comment
}

// DocumentFromIssue builds a normalized index.Document from a Jira Issue.
func DocumentFromIssue(iss Issue) index.Document {
	// Extract project from key (part before '-')
	project := iss.Key
	if idx := strings.Index(iss.Key, "-"); idx > 0 {
		project = iss.Key[:idx]
	}

	// Build a readable text rendering of the issue
	body := buildIssueBody(iss)

	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceJira,
		SourceID: iss.Key,
		Title:    iss.Title,
		Body:     body,
		Metadata: map[string]any{
			"jira_issue": iss,
			"jira_key":   iss.Key,
			"project":    project,
			"status":     iss.Status,
			"components": iss.Components,
			"labels":     iss.Labels,
			"service":    nil,
			"authority":  string(index.AuthorityMediumHigh),
		},
		CreatedAt: iss.Created,
		UpdatedAt: iss.Updated,
	}

	// Set BodyHash using the index package's Hash function
	doc.BodyHash = index.Hash(doc.Body)

	return doc
}

// buildIssueBody creates a readable text representation of the issue.
func buildIssueBody(iss Issue) string {
	var sb strings.Builder

	// Title
	fmt.Fprintf(&sb, "Jira: %s - %s\n", iss.Key, iss.Title)

	// Status
	if iss.Status != "" {
		fmt.Fprintf(&sb, "Status: %s\n", iss.Status)
	}

	// Components
	if len(iss.Components) > 0 {
		fmt.Fprintf(&sb, "Component: %s\n", strings.Join(iss.Components, ", "))
	}

	// Resolution
	if iss.Resolution != "" {
		fmt.Fprintf(&sb, "Resolution: %s\n", iss.Resolution)
	}

	// Timestamps
	if !iss.Created.IsZero() {
		fmt.Fprintf(&sb, "Created: %s\n", iss.Created.Format(time.RFC3339))
	}
	if !iss.Updated.IsZero() {
		fmt.Fprintf(&sb, "Updated: %s\n", iss.Updated.Format(time.RFC3339))
	}
	if !iss.Resolved.IsZero() {
		fmt.Fprintf(&sb, "Resolved: %s\n", iss.Resolved.Format(time.RFC3339))
	}

	// Assignee and Reporter
	if iss.Assignee != "" {
		fmt.Fprintf(&sb, "Assignee: %s\n", iss.Assignee)
	}
	if iss.Reporter != "" {
		fmt.Fprintf(&sb, "Reporter: %s\n", iss.Reporter)
	}

	// Labels
	if len(iss.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(iss.Labels, ", "))
	}

	sb.WriteString("\n")

	// Description
	if iss.Description != "" {
		fmt.Fprintf(&sb, "Description:\n%s\n\n", iss.Description)
	}

	// Comments summary
	if len(iss.Comments) > 0 {
		fmt.Fprintf(&sb, "Comments (%d):\n", len(iss.Comments))
		for i, c := range iss.Comments {
			fmt.Fprintf(&sb, "%d. %s (%s):\n%s\n\n", i+1, c.Author, c.Created.Format(time.RFC3339), c.Body)
		}
	}

	return sb.String()
}

// Chunker implements index.Chunker for Jira issues.
type Chunker struct{}

// New creates a new Chunker for Jira issues.
func New() *Chunker {
	return &Chunker{}
}

// Chunk turns an index.Document containing a Jira Issue into ordered Chunks.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	// Try to recover the structured Issue from metadata
	var iss Issue
	if rawIss, ok := doc.Metadata["jira_issue"]; ok {
		if issue, ok := rawIss.(Issue); ok {
			iss = issue
		}
	}

	// If we have the structured issue, produce typed chunks
	if iss.Key != "" {
		return c.chunkStructuredIssue(doc, iss)
	}

	// Fallback: chunk the Body as a single description chunk
	return c.chunkFallback(doc)
}

// chunkStructuredIssue produces chunks from a structured Jira Issue.
func (c *Chunker) chunkStructuredIssue(doc index.Document, iss Issue) ([]index.Chunk, error) {
	var chunks []index.Chunk
	var chunkIndex int

	// Create metadata for chunks (copy from doc, but strip the raw Issue)
	chunkMetadata := make(map[string]any)
	for k, v := range doc.Metadata {
		if k != "jira_issue" {
			chunkMetadata[k] = v
		}
	}

	// 1. Summary chunk
	summaryText := buildSummaryChunk(iss)
	if summaryText != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkJiraSummary,
			Title:      fmt.Sprintf("Summary: %s", iss.Key),
			Text:       summaryText,
			TextHash:   index.Hash(summaryText),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// 2. Description chunk (skip if empty)
	if iss.Description != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkJiraDescription,
			Title:      fmt.Sprintf("Description: %s", iss.Key),
			Text:       iss.Description,
			TextHash:   index.Hash(iss.Description),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// 3. Resolution chunk (only if Resolution is non-empty)
	if iss.Resolution != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkJiraResolution,
			Title:      fmt.Sprintf("Resolution: %s", iss.Key),
			Text:       iss.Resolution,
			TextHash:   index.Hash(iss.Resolution),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// 4. Comment chunks (group up to 8 comments per chunk, skip trivial)
	commentChunks := c.buildCommentChunks(iss, doc.ID, chunkMetadata, chunkIndex)
	chunks = append(chunks, commentChunks...)

	return chunks, nil
}

// buildSummaryChunk creates a normalized summary chunk text.
func buildSummaryChunk(iss Issue) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Jira: %s - %s\n", iss.Key, iss.Title)

	if iss.Status != "" {
		fmt.Fprintf(&sb, "Status: %s\n", iss.Status)
	}

	if len(iss.Components) > 0 {
		fmt.Fprintf(&sb, "Component: %s\n", strings.Join(iss.Components, ", "))
	}

	if iss.Resolution != "" {
		fmt.Fprintf(&sb, "Resolution: %s\n", iss.Resolution)
	}

	return strings.TrimSpace(sb.String())
}

// isTrivialComment checks if a comment body is trivial.
func isTrivialComment(body string) bool {
	trimmed := strings.TrimSpace(body)
	switch trimmed {
	case "ok", "done", "+1":
		return true
	}
	return false
}

// buildCommentChunks groups comments into chunks of up to 8, skipping trivial ones.
func (c *Chunker) buildCommentChunks(iss Issue, docID uuid.UUID, metadata map[string]any, startIndex int) []index.Chunk {
	if len(iss.Comments) == 0 {
		return nil
	}

	// Filter out trivial comments but keep track of substantive ones
	var substantiveComments []Comment
	for _, cmt := range iss.Comments {
		if !isTrivialComment(cmt.Body) {
			substantiveComments = append(substantiveComments, cmt)
		}
	}

	// If no substantive comments, return nothing
	if len(substantiveComments) == 0 {
		return nil
	}

	var chunks []index.Chunk
	chunkIndex := startIndex

	// Group comments into chunks of up to 8
	const commentsPerChunk = 8
	for i := 0; i < len(substantiveComments); i += commentsPerChunk {
		end := min(i+commentsPerChunk, len(substantiveComments))

		groupComments := substantiveComments[i:end]
		commentText := formatCommentGroup(groupComments)

		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: docID,
			Index:      chunkIndex,
			Type:       index.ChunkJiraComments,
			Title:      fmt.Sprintf("Comments: %s [%d-%d]", iss.Key, i+1, end),
			Text:       commentText,
			TextHash:   index.Hash(commentText),
			Metadata:   copyMetadata(metadata),
		})
		chunkIndex++
	}

	return chunks
}

// formatCommentGroup formats a group of comments as text.
func formatCommentGroup(comments []Comment) string {
	var sb strings.Builder

	for i, cmt := range comments {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&sb, "%s (%s):\n%s", cmt.Author, cmt.Created.Format(time.RFC3339), cmt.Body)
	}

	return sb.String()
}

// copyMetadata creates a shallow copy of metadata.
func copyMetadata(original map[string]any) map[string]any {
	cp := make(map[string]any)
	maps.Copy(cp, original)
	return cp
}

// chunkFallback handles the case where the structured Issue is not available.
func (c *Chunker) chunkFallback(doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	chunk := index.Chunk{
		ID:         uuid.New(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkJiraDescription,
		Title:      doc.Title,
		Text:       doc.Body,
		TextHash:   index.Hash(doc.Body),
		Metadata:   copyMetadata(doc.Metadata),
	}

	// Strip jira_issue from chunk metadata
	delete(chunk.Metadata, "jira_issue")

	return []index.Chunk{chunk}, nil
}
