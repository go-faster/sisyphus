// Package yaml implements a YAML manifest chunker: it splits multi-doc YAML
// streams into one chunk per Kubernetes resource and extracts kind/name/namespace
// metadata for structured retrieval.
package yaml

import (
	"bufio"
	"context"
	"maps"
	"strings"

	"github.com/go-faster/yaml"

	"github.com/go-faster/sisyphus/internal/index"
)

const (
	defaultMaxDocBytes = 64 * 1024
)

// Chunker splits a YAML document (possibly multi-doc) into chunks: one per
// Kubernetes resource, with metadata extracted from top-level keys.
type Chunker struct {
	maxDocBytes int
}

// ChunkerOptions configures a Chunker.
type ChunkerOptions struct {
	// MaxDocBytes is the maximum body bytes for a single YAML document before
	// skipping it entirely. 0 = 64 KB.
	MaxDocBytes int
}

func (opts *ChunkerOptions) setDefaults() {
	if opts.MaxDocBytes == 0 {
		opts.MaxDocBytes = defaultMaxDocBytes
	}
}

// New creates a new YAML chunker.
func New(opts ChunkerOptions) *Chunker {
	opts.setDefaults()
	return &Chunker{maxDocBytes: opts.MaxDocBytes}
}

// Chunk implements index.Chunker.
func (c *Chunker) Chunk(_ context.Context, doc index.Document) ([]index.Chunk, error) {
	if doc.Body == "" {
		return nil, nil
	}

	var chunks []index.Chunk
	chunkIdx := 0

	dec := yaml.NewDecoder(strings.NewReader(doc.Body))
	for {
		var raw yaml.Node
		if err := dec.Decode(&raw); err != nil {
			break
		}
		if raw.Kind != yaml.DocumentNode || len(raw.Content) == 0 {
			continue
		}

		root := raw.Content[0]

		// Serialize this individual document back to YAML text.
		docText, err := yaml.Marshal(root)
		if err != nil {
			continue
		}
		body := string(docText)

		if len(body) > c.maxDocBytes {
			continue
		}

		if isHelmRendered(body) {
			continue
		}

		kind := getNodeValue(root, "kind")

		if kind == "CustomResourceDefinition" {
			continue
		}

		meta := make(map[string]any)
		maps.Copy(meta, doc.Metadata)

		apiVersion := getNodeValue(root, "apiVersion")
		name := getNestedValue(root, "metadata", "name")
		namespace := getNestedValue(root, "metadata", "namespace")

		if apiVersion != "" {
			meta["apiVersion"] = apiVersion
		}
		if kind != "" {
			meta["kind"] = kind
		}
		if name != "" {
			meta["name"] = name
		}
		if namespace != "" {
			meta["namespace"] = namespace
		}

		title := kind
		if name != "" {
			title = kind + " " + name
		}
		if namespace != "" {
			title += " (" + namespace + ")"
		}
		if kind == "" {
			title = doc.Title
		}

		chunks = append(chunks, index.Chunk{
			ID:         index.NewID(),
			DocumentID: doc.ID,
			Index:      chunkIdx,
			Type:       index.ChunkManifest,
			Title:      title,
			Text:       body,
			TextHash:   index.Hash(body),
			Metadata:   meta,
		})
		chunkIdx++
	}

	return chunks, nil
}

func isHelmRendered(body string) bool {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "# Source:")
	}
	return false
}

func getNodeValue(n *yaml.Node, key string) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func getNestedValue(n *yaml.Node, keys ...string) string {
	current := n
	for _, key := range keys {
		if current.Kind != yaml.MappingNode {
			return ""
		}
		found := false
		for i := 0; i+1 < len(current.Content); i += 2 {
			if current.Content[i].Value == key {
				current = current.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return ""
		}
	}
	return current.Value
}
