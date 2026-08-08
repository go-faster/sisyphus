package files

import (
	"context"

	"go.uber.org/zap"
)

// Converter turns a document Markdown cannot be read out of directly - a Word
// file, a spreadsheet, a PDF - into Markdown.
//
// Every error it returns is per-document: [Walk] counts it, logs it and takes
// the next file. A converter that cannot run at all has to say so before the
// walk starts, because one that reports it per file would skip an entire
// corpus as a stream of warnings.
type Converter interface {
	// Supports reports whether ext, an extension with its leading dot, names
	// a format this converter handles.
	Supports(ext string) bool
	// Convert returns the Markdown for the document at path.
	Convert(ctx context.Context, path string) (string, error)
}

// Options configures [Walk].
type Options struct {
	// Converter, when set, converts the documents it Supports. Without one
	// they are skipped for not being UTF-8 text, which is the behavior of
	// every deployment that has not configured a converter.
	Converter Converter
	Logger    *zap.Logger
}

func (opts *Options) setDefaults() {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
}
