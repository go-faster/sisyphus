package main

import (
	"reflect"
	"testing"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestCleanSources(t *testing.T) {
	tests := []struct {
		name   string
		repo   string
		source string
		want   []index.Source
	}{
		{
			name: "none",
			want: nil,
		},
		{
			name: "repo",
			repo: "docs",
			want: []index.Source{
				index.SourceGitDocs("docs"),
				index.SourceGitManifest("docs"),
				index.SourceGitCode("docs"),
				index.SourceGitCommit("docs"),
				index.SourceGitTag("docs"),
			},
		},
		{
			name:   "source",
			source: "telegram",
			want:   []index.Source{index.SourceTelegram},
		},
		{
			name:   "dedupe",
			repo:   "docs",
			source: string(index.SourceGitDocs("docs")),
			want: []index.Source{
				index.SourceGitDocs("docs"),
				index.SourceGitManifest("docs"),
				index.SourceGitCode("docs"),
				index.SourceGitCommit("docs"),
				index.SourceGitTag("docs"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSources(tt.repo, tt.source)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("cleanSources() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
