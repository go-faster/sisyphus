// Package gitlab implements a GitLab chunker for issues, merge requests, and releases.
package gitlab

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/scpbot/internal/index"
)

// Comment represents a comment on a GitLab issue or MR.
type Comment struct {
	Author  string
	Body    string
	Created time.Time
}

// Issue models a GitLab issue.
type Issue struct {
	IID         int
	Title       string
	Description string
	State       string
	Labels      []string
	Author      string
	WebURL      string
	Created     time.Time
	Updated     time.Time
	Comments    []Comment
}

// MergeRequest models a GitLab merge request.
type MergeRequest struct {
	IID         int
	Title       string
	Description string
	State       string
	Labels      []string
	Author      string
	WebURL      string
	Created     time.Time
	Updated     time.Time
	Comments    []Comment
}

// Release models a GitLab release.
type Release struct {
	TagName     string
	Name        string
	Description string
	ReleasedAt  time.Time
	WebURL      string
}

// DocumentFromIssue builds a normalized index.Document from a GitLab Issue.
func DocumentFromIssue(project string, issue Issue) index.Document {
	sourceID := fmt.Sprintf("%s/issues/%d", project, issue.IID)
	body := buildIssueBody(issue)

	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabIssue,
		SourceID: sourceID,
		Title:    issue.Title,
		Body:     body,
		Metadata: map[string]any{
			"gitlab_issue": issue,
			"project":      project,
			"iid":          issue.IID,
			"state":        issue.State,
			"labels":       issue.Labels,
			"author":       issue.Author,
			"url":          issue.WebURL,
			"authority":    string(index.AuthorityMedium),
		},
		CreatedAt: issue.Created,
		UpdatedAt: issue.Updated,
	}

	doc.BodyHash = index.Hash(doc.Body)
	return doc
}

// DocumentFromMergeRequest builds a normalized index.Document from a GitLab MergeRequest.
func DocumentFromMergeRequest(project string, mr MergeRequest) index.Document {
	sourceID := fmt.Sprintf("%s/merge_requests/%d", project, mr.IID)
	body := buildMRBody(mr)

	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabMR,
		SourceID: sourceID,
		Title:    mr.Title,
		Body:     body,
		Metadata: map[string]any{
			"gitlab_mr": mr,
			"project":   project,
			"iid":       mr.IID,
			"state":     mr.State,
			"labels":    mr.Labels,
			"author":    mr.Author,
			"url":       mr.WebURL,
			"authority": string(index.AuthorityMedium),
		},
		CreatedAt: mr.Created,
		UpdatedAt: mr.Updated,
	}

	doc.BodyHash = index.Hash(doc.Body)
	return doc
}

// DocumentFromRelease builds a normalized index.Document from a GitLab Release.
func DocumentFromRelease(project string, release Release) index.Document {
	sourceID := fmt.Sprintf("%s/releases/%s", project, release.TagName)
	body := buildReleaseBody(release)

	doc := index.Document{
		ID:       uuid.New(),
		Source:   index.SourceGitLabRelease,
		SourceID: sourceID,
		Title:    release.Name,
		Body:     body,
		Metadata: map[string]any{
			"gitlab_release": release,
			"project":        project,
			"tag":            release.TagName,
			"url":            release.WebURL,
			"authority":      string(index.AuthorityMediumHigh),
		},
		CreatedAt: release.ReleasedAt,
		UpdatedAt: release.ReleasedAt,
	}

	doc.BodyHash = index.Hash(doc.Body)
	return doc
}

// buildIssueBody creates a readable text representation of an issue.
func buildIssueBody(issue Issue) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "GitLab Issue #%d - %s\n", issue.IID, issue.Title)

	if issue.State != "" {
		fmt.Fprintf(&sb, "State: %s\n", issue.State)
	}

	if len(issue.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}

	if issue.Author != "" {
		fmt.Fprintf(&sb, "Author: %s\n", issue.Author)
	}

	if !issue.Created.IsZero() {
		fmt.Fprintf(&sb, "Created: %s\n", issue.Created.Format(time.RFC3339))
	}
	if !issue.Updated.IsZero() {
		fmt.Fprintf(&sb, "Updated: %s\n", issue.Updated.Format(time.RFC3339))
	}

	sb.WriteString("\n")

	if issue.Description != "" {
		fmt.Fprintf(&sb, "Description:\n%s\n\n", issue.Description)
	}

	if len(issue.Comments) > 0 {
		fmt.Fprintf(&sb, "Comments (%d):\n", len(issue.Comments))
		for i, c := range issue.Comments {
			fmt.Fprintf(&sb, "%d. %s (%s):\n%s\n\n", i+1, c.Author, c.Created.Format(time.RFC3339), c.Body)
		}
	}

	return sb.String()
}

// buildMRBody creates a readable text representation of a merge request.
func buildMRBody(mr MergeRequest) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "GitLab MR !%d - %s\n", mr.IID, mr.Title)

	if mr.State != "" {
		fmt.Fprintf(&sb, "State: %s\n", mr.State)
	}

	if len(mr.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(mr.Labels, ", "))
	}

	if mr.Author != "" {
		fmt.Fprintf(&sb, "Author: %s\n", mr.Author)
	}

	if !mr.Created.IsZero() {
		fmt.Fprintf(&sb, "Created: %s\n", mr.Created.Format(time.RFC3339))
	}
	if !mr.Updated.IsZero() {
		fmt.Fprintf(&sb, "Updated: %s\n", mr.Updated.Format(time.RFC3339))
	}

	sb.WriteString("\n")

	if mr.Description != "" {
		fmt.Fprintf(&sb, "Description:\n%s\n\n", mr.Description)
	}

	if len(mr.Comments) > 0 {
		fmt.Fprintf(&sb, "Comments (%d):\n", len(mr.Comments))
		for i, c := range mr.Comments {
			fmt.Fprintf(&sb, "%d. %s (%s):\n%s\n\n", i+1, c.Author, c.Created.Format(time.RFC3339), c.Body)
		}
	}

	return sb.String()
}

// buildReleaseBody creates a readable text representation of a release.
func buildReleaseBody(release Release) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "GitLab Release: %s\n", release.TagName)

	if release.Name != "" && release.Name != release.TagName {
		fmt.Fprintf(&sb, "Name: %s\n", release.Name)
	}

	if !release.ReleasedAt.IsZero() {
		fmt.Fprintf(&sb, "Released: %s\n", release.ReleasedAt.Format(time.RFC3339))
	}

	sb.WriteString("\n")

	if release.Description != "" {
		fmt.Fprintf(&sb, "Release Notes:\n%s\n", release.Description)
	}

	return sb.String()
}

// Chunker implements index.Chunker for GitLab resources.
type Chunker struct{}

// New creates a new Chunker for GitLab resources.
func New() *Chunker {
	return &Chunker{}
}

// Chunk turns an index.Document containing a GitLab resource into ordered Chunks.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	switch doc.Source {
	case index.SourceGitLabIssue:
		return c.chunkIssue(doc)
	case index.SourceGitLabMR:
		return c.chunkMR(doc)
	case index.SourceGitLabRelease:
		return c.chunkRelease(doc)
	default:
		return c.chunkFallback(doc)
	}
}

// chunkIssue produces chunks from a GitLab Issue.
func (c *Chunker) chunkIssue(doc index.Document) ([]index.Chunk, error) {
	var issue Issue
	if rawIssue, ok := doc.Metadata["gitlab_issue"]; ok {
		if iss, ok := rawIssue.(Issue); ok {
			issue = iss
		}
	}

	var chunks []index.Chunk
	var chunkIndex int

	// Copy metadata, excluding the raw struct
	chunkMetadata := make(map[string]any)
	for k, v := range doc.Metadata {
		if k != "gitlab_issue" {
			chunkMetadata[k] = v
		}
	}

	// Summary chunk
	summaryText := buildIssueSummaryChunk(issue)
	if summaryText != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkGitLabIssueSummary,
			Title:      fmt.Sprintf("Summary: Issue #%d", issue.IID),
			Text:       summaryText,
			TextHash:   index.Hash(summaryText),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// Description chunk
	if issue.Description != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkGitLabIssueSummary,
			Title:      fmt.Sprintf("Description: Issue #%d", issue.IID),
			Text:       issue.Description,
			TextHash:   index.Hash(issue.Description),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// Comment chunks
	commentChunks := c.buildCommentChunks(issue.Comments, doc.ID, chunkMetadata, chunkIndex, "Issue")
	chunks = append(chunks, commentChunks...)

	return chunks, nil
}

// chunkMR produces chunks from a GitLab MergeRequest.
func (c *Chunker) chunkMR(doc index.Document) ([]index.Chunk, error) {
	var mr MergeRequest
	if rawMR, ok := doc.Metadata["gitlab_mr"]; ok {
		if m, ok := rawMR.(MergeRequest); ok {
			mr = m
		}
	}

	var chunks []index.Chunk
	var chunkIndex int

	// Copy metadata, excluding the raw struct
	chunkMetadata := make(map[string]any)
	for k, v := range doc.Metadata {
		if k != "gitlab_mr" {
			chunkMetadata[k] = v
		}
	}

	// Summary chunk
	summaryText := buildMRSummaryChunk(mr)
	if summaryText != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkGitLabMRSummary,
			Title:      fmt.Sprintf("Summary: MR !%d", mr.IID),
			Text:       summaryText,
			TextHash:   index.Hash(summaryText),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// Description chunk
	if mr.Description != "" {
		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			Index:      chunkIndex,
			Type:       index.ChunkGitLabMRSummary,
			Title:      fmt.Sprintf("Description: MR !%d", mr.IID),
			Text:       mr.Description,
			TextHash:   index.Hash(mr.Description),
			Metadata:   copyMetadata(chunkMetadata),
		})
		chunkIndex++
	}

	// Comment chunks
	commentChunks := c.buildCommentChunks(mr.Comments, doc.ID, chunkMetadata, chunkIndex, "MR")
	chunks = append(chunks, commentChunks...)

	return chunks, nil
}

// chunkRelease produces a single chunk from a GitLab Release.
func (c *Chunker) chunkRelease(doc index.Document) ([]index.Chunk, error) {
	var release Release
	if rawRelease, ok := doc.Metadata["gitlab_release"]; ok {
		if rel, ok := rawRelease.(Release); ok {
			release = rel
		}
	}

	chunkMetadata := make(map[string]any)
	for k, v := range doc.Metadata {
		if k != "gitlab_release" {
			chunkMetadata[k] = v
		}
	}

	text := release.Description
	if text == "" {
		text = release.Name
	}

	if text == "" {
		return nil, nil
	}

	chunk := index.Chunk{
		ID:         uuid.New(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkGitLabReleaseNotes,
		Title:      fmt.Sprintf("Release: %s", release.TagName),
		Text:       text,
		TextHash:   index.Hash(text),
		Metadata:   copyMetadata(chunkMetadata),
	}

	return []index.Chunk{chunk}, nil
}

// buildIssueSummaryChunk creates a summary chunk text for an issue.
func buildIssueSummaryChunk(issue Issue) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Issue #%d - %s\n", issue.IID, issue.Title)

	if issue.State != "" {
		fmt.Fprintf(&sb, "State: %s\n", issue.State)
	}

	if len(issue.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}

	return strings.TrimSpace(sb.String())
}

// buildMRSummaryChunk creates a summary chunk text for a merge request.
func buildMRSummaryChunk(mr MergeRequest) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "MR !%d - %s\n", mr.IID, mr.Title)

	if mr.State != "" {
		fmt.Fprintf(&sb, "State: %s\n", mr.State)
	}

	if len(mr.Labels) > 0 {
		fmt.Fprintf(&sb, "Labels: %s\n", strings.Join(mr.Labels, ", "))
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

// buildCommentChunks groups comments into chunks, skipping trivial ones.
func (c *Chunker) buildCommentChunks(comments []Comment, docID uuid.UUID, metadata map[string]any, startIndex int, resourceType string) []index.Chunk {
	if len(comments) == 0 {
		return nil
	}

	// Filter out trivial comments
	var substantiveComments []Comment
	for _, cmt := range comments {
		if !isTrivialComment(cmt.Body) {
			substantiveComments = append(substantiveComments, cmt)
		}
	}

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

		chunkType := index.ChunkGitLabIssueComments
		if resourceType == "MR" {
			chunkType = index.ChunkGitLabMRComments
		}

		chunks = append(chunks, index.Chunk{
			ID:         uuid.New(),
			DocumentID: docID,
			Index:      chunkIndex,
			Type:       chunkType,
			Title:      fmt.Sprintf("Comments [%d-%d]", i+1, end),
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

// chunkFallback handles unknown document types.
func (c *Chunker) chunkFallback(doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	chunk := index.Chunk{
		ID:         uuid.New(),
		DocumentID: doc.ID,
		Index:      0,
		Type:       index.ChunkSection,
		Title:      doc.Title,
		Text:       doc.Body,
		TextHash:   index.Hash(doc.Body),
		Metadata:   copyMetadata(doc.Metadata),
	}

	return []index.Chunk{chunk}, nil
}
