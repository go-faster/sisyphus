package stub

import (
	"testing"

	"github.com/go-faster/sisyphus/internal/index"
)

// TestSummarizerBasic tests basic summarization without truncation.
func TestSummarizerBasic(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	prompt := "This is a simple test. It should work."
	result, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	// Should return the entire text without truncation suffix.
	if result != "This is a simple test. It should work." {
		t.Errorf("got %q, want %q", result, "This is a simple test. It should work.")
	}
}

// TestSummarizerEmptyPrompt tests empty string input.
func TestSummarizerEmptyPrompt(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	result, err := s.Summarize(ctx, "")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if result != "" {
		t.Errorf("got %q, want empty string", result)
	}
}

// TestSummarizerWhitespaceOnly tests whitespace-only input.
func TestSummarizerWhitespaceOnly(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	result, err := s.Summarize(ctx, "   \n  \t  ")
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if result != "" {
		t.Errorf("got %q, want empty string", result)
	}
}

// TestSummarizerSentenceTruncation tests truncation at 3 sentences.
func TestSummarizerSentenceTruncation(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	// Five sentences; should truncate to first three.
	prompt := "First sentence. Second sentence. Third sentence. Fourth sentence. Fifth sentence."
	result, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	// Should have the first three sentences plus truncation suffix.
	expected := "First sentence. Second sentence. Third sentence. …"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// TestSummarizerRuneTruncation tests truncation at ~500 runes.
func TestSummarizerRuneTruncation(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	// Create a prompt with a few sentences that exceed 500 runes.
	// Each sentence is ~180 runes, so 3 sentences will be ~540 runes.
	sentence := "This is a sentence with enough content to test the rune limit. " +
		"It contains multiple words and should help us verify the 500 rune threshold. " +
		"The sentence continues with more content to fill up the character count. " +
		"And even more words here to ensure we hit the limit properly."
	prompt := sentence + " " + sentence + " " + sentence

	result, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	// Should be truncated and have the suffix.
	if !hasPrefix(result, "This is a sentence") {
		t.Errorf("result doesn't start with expected text: %q", result)
	}
	if !hasSuffix(result, " …") {
		t.Errorf("result doesn't end with truncation suffix: %q", result)
	}

	// Check that it doesn't exceed a reasonable rune count (500 + some buffer for the suffix).
	runeCount := len([]rune(result))
	if runeCount > 550 {
		t.Errorf("result has %d runes, should be <= 550", runeCount)
	}
}

// TestSummarizerMultipleSentenceTypes tests different sentence delimiters.
func TestSummarizerMultipleSentenceTypes(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	prompt := "First sentence. Second question? Third exclamation! Fourth. Fifth."
	result, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	// Should stop after 3 sentences.
	if !hasPrefix(result, "First sentence. Second question? Third exclamation!") {
		t.Errorf("got %q", result)
	}
	if !hasSuffix(result, " …") {
		t.Errorf("result doesn't end with truncation suffix: %q", result)
	}
}

// TestSummarizerWhitespaceCollapse tests whitespace normalization.
func TestSummarizerWhitespaceCollapse(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	prompt := "  This   has   extra    whitespace.  \n\n  Another sentence.  "
	result, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	// Should collapse whitespace but preserve sentence structure.
	expected := "This has extra whitespace. Another sentence."
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// TestSummarizerDeterminism ensures same input produces same output.
func TestSummarizerDeterminism(t *testing.T) {
	s := NewSummarizer()
	ctx := t.Context()

	prompt := "First sentence. Second sentence. Third sentence. Fourth. Fifth."
	result1, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	result2, err := s.Summarize(ctx, prompt)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	if result1 != result2 {
		t.Errorf("non-deterministic: %q != %q", result1, result2)
	}
}

// TestAnswererEmptyResults tests answering with no results.
func TestAnswererEmptyResults(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	question := "What is the meaning of life?"
	result, err := a.Answer(ctx, index.Query{Text: question}, []index.Result{})
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if !hasPrefix(result.Text, "Question: What is the meaning of life?") {
		t.Errorf("missing question header in %q", result.Text)
	}
	if !contains(result.Text, "No relevant context was found") {
		t.Errorf("missing 'no context' message in %q", result.Text)
	}
	if !hasSuffix(result.Text, "Confidence: low (LLM disabled)") {
		t.Errorf("missing confidence footer in %q", result.Text)
	}
}

// TestAnswererSingleResult tests answering with one result.
func TestAnswererSingleResult(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk := index.Chunk{
		Title: "Go Basics",
		Text:  "Go is a compiled, statically typed language designed for simplicity and efficiency.",
		Metadata: map[string]any{
			"source":     "docs",
			"source_url": "https://golang.org",
		},
	}
	result := index.Result{Chunk: chunk, Score: 0.95, Vector: true}

	answer, err := a.Answer(ctx, index.Query{Text: "What is Go?"}, []index.Result{result})
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if !hasPrefix(answer.Text, "Question: What is Go?") {
		t.Errorf("missing question header in %q", answer)
	}
	if !contains(answer.Text, "1. Go Basics") {
		t.Errorf("missing numbered title in %q", answer)
	}
	if !contains(answer.Text, "[source: docs]") {
		t.Errorf("missing source metadata in %q", answer)
	}
	if !contains(answer.Text, "[https://golang.org]") {
		t.Errorf("missing source_url in %q", answer)
	}
	if !contains(answer.Text, "Go is a compiled, statically typed language") {
		t.Errorf("missing snippet in %q", answer)
	}
	if !hasSuffix(answer.Text, "Confidence: low (LLM disabled)") {
		t.Errorf("missing confidence footer in %q", answer)
	}
}

// TestAnswererMultipleResults tests answering with multiple results.
func TestAnswererMultipleResults(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk1 := index.Chunk{
		Title: "First Source",
		Text:  "This is the content of the first source.",
		Metadata: map[string]any{
			"source": "docs",
		},
	}
	chunk2 := index.Chunk{
		Title: "Second Source",
		Text:  "This is the content of the second source with more information.",
		Metadata: map[string]any{
			"source":     "wiki",
			"source_url": "https://wiki.example.com",
		},
	}

	results := []index.Result{
		{Chunk: chunk1, Score: 0.9},
		{Chunk: chunk2, Score: 0.85},
	}

	answer, err := a.Answer(ctx, index.Query{Text: "Tell me about sources"}, results)
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if !contains(answer.Text, "1. First Source") {
		t.Errorf("missing first numbered source in %q", answer)
	}
	if !contains(answer.Text, "2. Second Source") {
		t.Errorf("missing second numbered source in %q", answer)
	}
	if !contains(answer.Text, "This is the content of the first source.") {
		t.Errorf("missing first snippet in %q", answer)
	}
	if !contains(answer.Text, "This is the content of the second source") {
		t.Errorf("missing second snippet in %q", answer)
	}
}

// TestAnswererSnippetTruncation tests that long chunk text is truncated to ~200 runes.
func TestAnswererSnippetTruncation(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	// Create a long text (well over 200 runes).
	longText := "This is a very long piece of text that should be truncated to approximately 200 runes. " +
		"It contains multiple sentences and words to ensure we properly test the truncation logic. " +
		"The snippet should be cut off at the right point with an ellipsis to indicate truncation. " +
		"There's much more content here that should not appear in the final answer."

	chunk := index.Chunk{
		Title: "Long Text",
		Text:  longText,
		Metadata: map[string]any{
			"source": "test",
		},
	}
	result := index.Result{Chunk: chunk}

	answer, err := a.Answer(ctx, index.Query{Text: "What about this?"}, []index.Result{result})
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	// Check that the snippet is truncated.
	if !contains(answer.Text, "…") {
		t.Errorf("snippet not truncated with ellipsis in %q", answer)
	}

	// The "very long" text should be present, but "much more content" should likely not be.
	if !contains(answer.Text, "very long") {
		t.Errorf("missing start of snippet in %q", answer)
	}
}

// TestAnswererNoMetadata tests result without metadata fields.
func TestAnswererNoMetadata(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk := index.Chunk{
		Title: "Title Only",
		Text:  "Some text content.",
		// No metadata
	}
	result := index.Result{Chunk: chunk}

	answer, err := a.Answer(ctx, index.Query{Text: "Question?"}, []index.Result{result})
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if !contains(answer.Text, "1. Title Only") {
		t.Errorf("missing title in %q", answer)
	}
	if !contains(answer.Text, "Some text content.") {
		t.Errorf("missing text in %q", answer)
	}
}

// TestAnswererNoTitle tests result without title.
func TestAnswererNoTitle(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk := index.Chunk{
		Title: "",
		Text:  "Text without a title.",
		Metadata: map[string]any{
			"source": "docs",
		},
	}
	result := index.Result{Chunk: chunk}

	answer, err := a.Answer(ctx, index.Query{Text: "Question?"}, []index.Result{result})
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if !contains(answer.Text, "1. (Untitled)") {
		t.Errorf("missing untitled fallback in %q", answer)
	}
}

// TestAnswererDeterminism ensures same inputs produce same output.
func TestAnswererDeterminism(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk := index.Chunk{
		Title: "Test",
		Text:  "Content here.",
		Metadata: map[string]any{
			"source": "test",
		},
	}
	result := index.Result{Chunk: chunk}
	results := []index.Result{result}

	answer1, err := a.Answer(ctx, index.Query{Text: "Question?"}, results)
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	answer2, err := a.Answer(ctx, index.Query{Text: "Question?"}, results)
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	if answer1.Text != answer2.Text {
		t.Errorf("non-deterministic:\n%q\n!=\n%q", answer1.Text, answer2.Text)
	}
}

// TestAnswererStableOrdering ensures results maintain their input order.
func TestAnswererStableOrdering(t *testing.T) {
	a := NewAnswerer()
	ctx := t.Context()

	chunk1 := index.Chunk{Title: "First"}
	chunk2 := index.Chunk{Title: "Second"}
	chunk3 := index.Chunk{Title: "Third"}

	results := []index.Result{
		{Chunk: chunk1},
		{Chunk: chunk2},
		{Chunk: chunk3},
	}

	answer, err := a.Answer(ctx, index.Query{Text: "Q?"}, results)
	if err != nil {
		t.Fatalf("Answer failed: %v", err)
	}

	// Check that numbering is in input order.
	idx1 := findIndex(answer.Text, "1. First")
	idx2 := findIndex(answer.Text, "2. Second")
	idx3 := findIndex(answer.Text, "3. Third")

	if idx1 < 0 || idx2 < 0 || idx3 < 0 {
		t.Errorf("not all results found in answer")
	}

	if idx1 >= idx2 || idx2 >= idx3 {
		t.Errorf("results not in stable order")
	}
}

// Helper functions
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func contains(s, substring string) bool {
	return s != "" && substring != "" && findIndex(s, substring) >= 0
}

func findIndex(s, substring string) int {
	for i := 0; i <= len(s)-len(substring); i++ {
		if s[i:i+len(substring)] == substring {
			return i
		}
	}
	return -1
}
