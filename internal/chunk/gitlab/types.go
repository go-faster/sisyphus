// Package gitlab implements a GitLab chunker for issues, merge requests, and releases.
package gitlab

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-faster/sisyphus/internal/index"
)

// Comment represents a comment on a GitLab issue or MR.
type Comment struct {
	Author  string
	Body    string
	Created time.Time
}

// Thread groups comments from a discussion, tracking resolved state.
type Thread struct {
	ID       string
	Resolved bool
	Comments []Comment
}

// Link represents a cross-reference between issues/MRs.
type Link struct {
	Type       string // "relates_to", "blocks", "closes"
	TargetKind string // "issue" or "merge_request"
	TargetIID  int
	Title      string
	WebURL     string
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
	Assignees   []string
	Threads     []Thread
	Links       []Link
}

// MergeRequest models a GitLab merge request.
type MergeRequest struct {
	IID            int
	Title          string
	Description    string
	State          string
	Labels         []string
	Author         string
	WebURL         string
	Created        time.Time
	Updated        time.Time
	Assignees      []string
	Reviewers      []string
	Draft          bool
	TargetBranch   string
	SourceBranch   string
	MergedAt       time.Time
	MergedBy       string
	MergeCommitSHA string
	Threads        []Thread
	Links          []Link
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

	if len(issue.Assignees) > 0 {
		fmt.Fprintf(&sb, "Assignees: %s\n", strings.Join(issue.Assignees, ", "))
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

	if len(issue.Links) > 0 {
		fmt.Fprintf(&sb, "Related:\n")
		for _, link := range issue.Links {
			fmt.Fprintf(&sb, "- %s #%d: %s (%s)\n", link.Type, link.TargetIID, link.Title, link.WebURL)
		}
		sb.WriteString("\n")
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

	if len(mr.Assignees) > 0 {
		fmt.Fprintf(&sb, "Assignees: %s\n", strings.Join(mr.Assignees, ", "))
	}

	if len(mr.Reviewers) > 0 {
		fmt.Fprintf(&sb, "Reviewers: %s\n", strings.Join(mr.Reviewers, ", "))
	}

	if mr.Draft {
		fmt.Fprintf(&sb, "Draft: yes\n")
	}

	if mr.SourceBranch != "" && mr.TargetBranch != "" {
		fmt.Fprintf(&sb, "Branch: %s -> %s\n", mr.SourceBranch, mr.TargetBranch)
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

	if !mr.MergedAt.IsZero() {
		fmt.Fprintf(&sb, "Merged: %s by %s\n", mr.MergedAt.Format(time.RFC3339), mr.MergedBy)
		if mr.MergeCommitSHA != "" {
			fmt.Fprintf(&sb, "Merge commit: %s\n", mr.MergeCommitSHA)
		}
	}

	sb.WriteString("\n")

	if mr.Description != "" {
		fmt.Fprintf(&sb, "Description:\n%s\n\n", mr.Description)
	}

	if len(mr.Links) > 0 {
		fmt.Fprintf(&sb, "Related:\n")
		for _, link := range mr.Links {
			fmt.Fprintf(&sb, "- %s #%d: %s (%s)\n", link.Type, link.TargetIID, link.Title, link.WebURL)
		}
		sb.WriteString("\n")
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

	// Thread chunks
	threadChunks := c.buildThreadChunks(issue.Threads, doc.ID, chunkMetadata, chunkIndex, "Issue")
	chunks = append(chunks, threadChunks...)

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

	// Thread chunks
	threadChunks := c.buildThreadChunks(mr.Threads, doc.ID, chunkMetadata, chunkIndex, "MR")
	chunks = append(chunks, threadChunks...)

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

	if len(issue.Assignees) > 0 {
		fmt.Fprintf(&sb, "Assignees: %s\n", strings.Join(issue.Assignees, ", "))
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

	if len(mr.Assignees) > 0 {
		fmt.Fprintf(&sb, "Assignees: %s\n", strings.Join(mr.Assignees, ", "))
	}

	if len(mr.Reviewers) > 0 {
		fmt.Fprintf(&sb, "Reviewers: %s\n", strings.Join(mr.Reviewers, ", "))
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

// buildThreadChunks groups threads into chunks, filtering trivial comments within threads.
func (c *Chunker) buildThreadChunks(threads []Thread, docID uuid.UUID, metadata map[string]any, startIndex int, resourceType string) []index.Chunk {
	if len(threads) == 0 {
		return nil
	}

	// Filter threads: for each thread, filter out trivial comments
	// and drop threads that have no substantive comments after filtering.
	var substantiveThreads []Thread
	for _, thread := range threads {
		var substantiveComments []Comment
		for _, cmt := range thread.Comments {
			if !isTrivialComment(cmt.Body) {
				substantiveComments = append(substantiveComments, cmt)
			}
		}
		// Only include thread if it has substantive comments
		if len(substantiveComments) > 0 {
			thread.Comments = substantiveComments
			substantiveThreads = append(substantiveThreads, thread)
		}
	}

	if len(substantiveThreads) == 0 {
		return nil
	}

	var chunks []index.Chunk
	chunkIndex := startIndex

	// Group threads into chunks of up to 8
	const threadsPerChunk = 8
	for i := 0; i < len(substantiveThreads); i += threadsPerChunk {
		end := min(i+threadsPerChunk, len(substantiveThreads))

		groupThreads := substantiveThreads[i:end]
		threadText := formatThreadGroup(groupThreads)

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
			Text:       threadText,
			TextHash:   index.Hash(threadText),
			Metadata:   copyMetadata(metadata),
		})
		chunkIndex++
	}

	return chunks
}

// formatThreadGroup formats a group of threads as text.
func formatThreadGroup(threads []Thread) string {
	var sb strings.Builder

	for i, thread := range threads {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}

		// Prefix with [resolved] if the thread is resolved
		if thread.Resolved {
			sb.WriteString("[resolved] ")
		}

		// Format all comments in this thread
		for j, cmt := range thread.Comments {
			if j > 0 {
				sb.WriteString("\n\n")
			}
			fmt.Fprintf(&sb, "%s (%s):\n%s", cmt.Author, cmt.Created.Format(time.RFC3339), cmt.Body)
		}
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
