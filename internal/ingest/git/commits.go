package git

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"

	"github.com/go-faster/scpbot/internal/index"
)

// CommitCursor tracks incremental commit ingestion state.
type CommitCursor struct {
	LastSHA string `json:"last_sha"` // Stop when reaching this SHA (exclusive)
	Branch  string `json:"branch"`   // The branch being tracked
}

// CommitResult holds the documents and next cursor after a WalkCommits run.
type CommitResult struct {
	Documents  []index.Document
	NextCursor CommitCursor
}

// WalkCommits walks commit messages on the default branch incrementally.
// It opens the repo at s.Root, walks the log from HEAD back to cur.LastSHA (exclusive),
// newest-first, applying filters and the limit (0 = unlimited).
// NextCursor.LastSHA is set to the HEAD sha so the next run stops there.
func WalkCommits(ctx context.Context, s Source, cur CommitCursor, limit int) (CommitResult, error) {
	lg := zctx.From(ctx)

	repo, err := git.PlainOpen(s.Root)
	if err != nil {
		return CommitResult{}, errors.Wrap(err, "open repository")
	}

	head, err := repo.Head()
	if err != nil {
		return CommitResult{}, errors.Wrap(err, "get HEAD")
	}

	// Determine the branch name
	branchName := s.Branch
	if branchName == "" {
		// Use the short name from HEAD ref (e.g., "main" from "refs/heads/main")
		branchName = head.Name().Short()
	}

	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return CommitResult{}, errors.Wrap(err, "log")
	}
	defer iter.Close()

	var docs []index.Document
	count := 0
	headSHA := head.Hash().String()

	err = iter.ForEach(func(commit *object.Commit) error {
		// Stop if we've reached the last cursor position (exclusive)
		if commit.Hash.String() == cur.LastSHA {
			return io.EOF
		}

		// Skip merge commits
		if commit.NumParents() > 1 {
			return nil
		}

		// Skip bot-authored commits
		if isBotAuthor(commit.Author.Name, commit.Author.Email) {
			return nil
		}

		// Parse subject and body.
		subject, rest, _ := strings.Cut(commit.Message, "\n")
		subject = strings.TrimSpace(subject)

		// Skip trivial messages
		if isTrivialSubject(subject) {
			return nil
		}

		// Build body: subject + "\n\n" + remaining message (trim trailing space).
		body := subject
		if rest = strings.TrimSpace(rest); rest != "" {
			body = subject + "\n\n" + rest
		}

		shortSHA := commit.Hash.String()[:12]
		source := index.SourceGitCommit(s.Repo)
		meta := map[string]any{
			"source":       string(source),
			"repo":         s.Repo,
			"sha":          commit.Hash.String(),
			"author":       commit.Author.Name,
			"author_email": commit.Author.Email,
			"branch":       branchName,
			"authority":    string(index.AuthorityLow),
		}

		doc := index.Document{
			ID:        index.NewID(),
			Source:    source,
			SourceID:  s.Repo + "@" + shortSHA,
			Title:     subject,
			Body:      body,
			BodyHash:  index.Hash(body),
			Metadata:  meta,
			CreatedAt: commit.Author.When,
			UpdatedAt: commit.Author.When,
		}

		docs = append(docs, doc)
		count++

		if limit > 0 && count >= limit {
			// Signal to stop the iteration
			return io.EOF
		}

		return nil
	})

	// io.EOF is used to signal early exit due to cursor or limit
	if err != nil && !errors.Is(err, io.EOF) {
		return CommitResult{}, errors.Wrap(err, "walk commits")
	}

	lg.Info("walked commits",
		zap.String("repo", s.Repo),
		zap.Int("count", len(docs)),
		zap.String("head", headSHA[:12]))

	return CommitResult{
		Documents: docs,
		NextCursor: CommitCursor{
			LastSHA: headSHA,
			Branch:  branchName,
		},
	}, nil
}

// isBotAuthor checks if the author name or email looks like a bot.
func isBotAuthor(name, email string) bool {
	nameLower := strings.ToLower(name)
	emailLower := strings.ToLower(email)

	botPatterns := []string{
		"bot", "[bot]", "noreply", "dependabot", "renovate",
	}

	for _, pattern := range botPatterns {
		if strings.Contains(nameLower, pattern) || strings.Contains(emailLower, pattern) {
			return true
		}
	}

	return false
}

// trivialSubjectPatterns match commit subjects that carry no useful context.
var trivialSubjectPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\.{3}$`),
	regexp.MustCompile(`^-+$`),
	regexp.MustCompile(`^wip`),
}

// isTrivialSubject checks if a commit subject is trivial or empty.
func isTrivialSubject(subject string) bool {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return true
	}
	for _, re := range trivialSubjectPatterns {
		if re.MatchString(subject) {
			return true
		}
	}
	return false
}

// UnmarshalCursor decodes a JSON-encoded cursor or returns a zero cursor if the input is nil/empty.
func UnmarshalCursor(data []byte) (CommitCursor, error) {
	if len(data) == 0 {
		return CommitCursor{}, nil
	}

	var cur CommitCursor
	if err := json.Unmarshal(data, &cur); err != nil {
		return CommitCursor{}, errors.Wrap(err, "unmarshal cursor")
	}

	return cur, nil
}

// MarshalCursor encodes a cursor to JSON.
func MarshalCursor(cur CommitCursor) ([]byte, error) {
	data, err := json.Marshal(cur)
	if err != nil {
		return nil, errors.Wrap(err, "marshal cursor")
	}
	return data, nil
}
