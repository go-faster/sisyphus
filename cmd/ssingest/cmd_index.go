package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	entagg "github.com/go-faster/sisyphus/internal/ent"
	"github.com/go-faster/sisyphus/internal/ent/document"
	"github.com/go-faster/sisyphus/internal/index"
)

func newIndexCmd(deps *ingestDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "inspect and clean indexed documents",
	}
	cmd.AddCommand(
		newIndexListCmd(deps),
		newIndexCleanCmd(deps),
	)
	return cmd
}

func newIndexListCmd(deps *ingestDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list indexed sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listIndexSources(cmd.Context(), cmd.OutOrStdout(), deps)
		},
	}
}

func newIndexCleanCmd(deps *ingestDeps) *cobra.Command {
	var (
		yes     bool
		answers bool
		repo    string
		source  string
	)
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "delete indexed documents for selected sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("refusing to clean index without --yes")
			}
			sources := cleanSources(answers, repo, source)
			if len(sources) == 0 {
				return errors.New("select at least one of --answers, --repo, or --source")
			}
			for _, src := range sources {
				if err := resetSource(cmd.Context(), deps.services.DB, deps.services.Vectors, src); err != nil {
					return errors.Wrap(err, "clean "+string(src))
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cleaned %s\n", src)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion")
	cmd.Flags().BoolVar(&answers, "answers", false, "delete indexed answered questions")
	cmd.Flags().StringVar(&repo, "repo", "", "delete all git-derived sources for a repo")
	cmd.Flags().StringVar(&source, "source", "", "delete one exact source, e.g. git_docs:repo")
	return cmd
}

func listIndexSources(ctx context.Context, w io.Writer, deps *ingestDeps) error {
	type row struct {
		Source string `json:"source,omitempty"`
		Count  int    `json:"count,omitempty"`
	}
	var rows []row
	if err := deps.services.DB.Document.Query().
		GroupBy(document.FieldSource).
		Aggregate(entagg.Count()).
		Scan(ctx, &rows); err != nil {
		return errors.Wrap(err, "list indexed sources")
	}

	_, _ = fmt.Fprintln(w, "SOURCE\tDOCUMENTS")
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%s\t%d\n", r.Source, r.Count)
	}
	return nil
}

func cleanSources(answers bool, repo, source string) []index.Source {
	seen := map[index.Source]bool{}
	var out []index.Source
	add := func(src index.Source) {
		if src == "" || seen[src] {
			return
		}
		seen[src] = true
		out = append(out, src)
	}

	if answers {
		add(index.SourceAnswer)
	}
	if repo = strings.TrimSpace(repo); repo != "" {
		add(index.SourceGitDocs(repo))
		add(index.SourceGitManifest(repo))
		add(index.SourceGitCode(repo))
		add(index.SourceGitCommit(repo))
		add(index.SourceGitTag(repo))
	}
	if source = strings.TrimSpace(source); source != "" {
		add(index.Source(source))
	}
	return out
}
