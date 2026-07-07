package api

import "github.com/go-faster/sisyphus/internal/index"

const (
	sourceTierCurated = "curated"
	sourceTierCode    = "code"
	sourceTierHistory = "history"
	sourceTierAll     = "all"
)

var sourceTierPrefixes = map[string][]string{
	sourceTierCurated: {
		string(index.SourceAnswer),
		index.SourceContextFilesPrefix,
		index.SourceGitDocsPrefix,
		index.SourceGitManifestPrefix,
		string(index.SourceJira),
		string(index.SourceGitLabIssue),
		string(index.SourceGitLabMR),
		string(index.SourceGitLabRelease),
	},
	sourceTierCode: {
		index.SourceGitCodePrefix,
		index.SourceGitManifestPrefix,
	},
	sourceTierHistory: {
		index.SourceGitCommitsPrefix,
		index.SourceGitTagsPrefix,
		string(index.SourceGitLabIssue),
		string(index.SourceGitLabMR),
		string(index.SourceTelegram),
	},
	sourceTierAll: nil,
}

func sourcePrefixes(filters map[string]string, sourceTier string, explicit []string) []string {
	if filters != nil && filters["source"] != "" {
		return nil
	}
	if len(explicit) > 0 {
		return explicit
	}
	if sourceTier == "" {
		sourceTier = sourceTierCurated
	}
	prefixes, ok := sourceTierPrefixes[sourceTier]
	if !ok {
		return sourceTierPrefixes[sourceTierCurated]
	}
	return prefixes
}
