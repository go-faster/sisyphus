package main

import (
	"reflect"
	"testing"

	"github.com/go-faster/sisyphus/internal/index"
)

func TestCleanSources(t *testing.T) {
	tests := []struct {
		name    string
		answers bool
		repo    string
		source  string
		want    []index.Source
	}{
		{
			name:    "answers",
			answers: true,
			want:    []index.Source{index.SourceAnswer},
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
			name:    "dedupe",
			answers: true,
			source:  string(index.SourceAnswer),
			want:    []index.Source{index.SourceAnswer},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSources(tt.answers, tt.repo, tt.source)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("cleanSources() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
