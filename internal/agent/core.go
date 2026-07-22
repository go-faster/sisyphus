package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// ErrMaxIterations is the sentinel coreLoop's returned error matches (via
// errors.Is) when the model never calls the terminal tool within the
// configured iteration budget (plus the grace attempt). Callers can check
// against it to distinguish exhausted iterations from other failures (e.g. a
// context timeout).
var ErrMaxIterations = errors.New("exceeded max iterations")

// maxIterationsError carries the configured budget in its message while
// still matching ErrMaxIterations via errors.Is, so the message text stays
// exactly "exceeded max iterations (N)" (errors.Wrap would put the
// sentinel's text first instead).
type maxIterationsError struct {
	max int
}

func (e *maxIterationsError) Error() string {
	return fmt.Sprintf("exceeded max iterations (%d)", e.max)
}

func (e *maxIterationsError) Is(target error) bool {
	return target == ErrMaxIterations
}

// TerminalTool describes the submit tool that ends the loop.
type TerminalTool struct {
	Name       string
	Def        openai.ChatCompletionToolUnionParam
	Parse      func(argsJSON string) (terminal bool, err error)
	SuccessMsg string
	ErrMsg     func(err error) string
}

// coreResult holds the loop's raw output for the caller to interpret.
type coreResult struct {
	Iterations       int
	ToolsUsed        int
	Conversation     []openai.ChatCompletionMessageParamUnion
	Tools            []openai.ChatCompletionToolUnionParam
	TerminalArgs     string
	NoToolContent    string
	DiscoveredURLs   map[string]struct{}
	TraceID          string
	DurationMS       int64
	PromptTokens     int64
	CompletionTokens int64
}

// coreLoop runs the generic LLM ↔ tool-calling loop until the terminal tool
// is called or maxIterations is reached (plus one grace attempt to let the
// model wrap up after a warning — see the loop body below).
func coreLoop(ctx context.Context, llm LLM, toolSource ToolSource, model string,
	messages []openai.ChatCompletionMessageParamUnion,
	tools []openai.ChatCompletionToolUnionParam,
	terminal TerminalTool,
	maxIterations int,
	logger *zap.Logger,
) (coreResult, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	span := trace.SpanFromContext(ctx)
	start := time.Now()

	var res coreResult
	res.Tools = tools
	res.DiscoveredURLs = make(map[string]struct{})
	if sc := span.SpanContext(); sc.IsValid() {
		res.TraceID = sc.TraceID().String()
	}

	// Every tool result is untrusted content (ingested chunks, fetched pages,
	// raw shell output) fed back to the model as a ToolMessage. buildSeedMessages
	// (internal/answer/framing.go) fences the *seed* search results with a random
	// delimiter tag so the model can visually distinguish data from instructions;
	// mid-loop results need the same treatment since they're the more likely
	// vector for a prompt-injection payload (arbitrary fetched/shell content, not
	// curated seed chunks). One tag is generated per loop run and reused for every
	// tool result in the conversation.
	tag, err := randomTag()
	if err != nil {
		res.DurationMS = time.Since(start).Milliseconds()
		return res, errors.Wrap(err, "generate delimiter tag")
	}

	// One grace attempt runs past maxIterations: the model gets a heads-up on
	// the last regular iteration ("wrap up soon"), then a forced-finish
	// instruction on the extra attempt, instead of being cut off mid-thought
	// with no chance to submit whatever it has.
	totalAttempts := maxIterations + 1
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		res.Iterations++
		span.AddEvent("agent.iteration", trace.WithAttributes(attribute.Int("iteration", res.Iterations)))

		switch attempt {
		case maxIterations:
			messages = append(messages, openai.UserMessage(fmt.Sprintf(
				"Reminder: you have 1 iteration left after this one before the task is stopped. "+
					"Start wrapping up and call %s soon.", terminal.Name)))
		case totalAttempts:
			messages = append(messages, openai.UserMessage(fmt.Sprintf(
				"This is your final iteration: you must call %s now with your best available answer.",
				terminal.Name)))
		}

		msg, usage, err := llm.CompleteWithTools(ctx, model, messages, tools)
		if err != nil {
			res.DurationMS = time.Since(start).Milliseconds()
			return res, errors.Wrap(err, "complete with tools")
		}
		res.PromptTokens += usage.PromptTokens
		res.CompletionTokens += usage.CompletionTokens
		messages = append(messages, toParamWithReasoning(msg))

		if len(msg.ToolCalls) == 0 {
			res.NoToolContent = msg.Content
			res.Conversation = messages
			res.DurationMS = time.Since(start).Milliseconds()
			span.AddEvent("agent." + terminal.Name)
			return res, nil
		}

		// Pass 1 (sequential, no I/O): find the first valid terminal call,
		// exactly mirroring the old single-pass control flow's continue/break
		// semantics. Any further tool calls in the same message (terminal or
		// not) after that point are left unexecuted rather than risk an
		// incoherent state (e.g. a second, unparseable terminal call
		// overwriting a captured TerminalArgs). Invalid terminal-call
		// attempts still get an error ToolMessage and scanning continues.
		// Regular (non-terminal) calls before the terminal are only recorded
		// here; they're executed concurrently in pass 2 below.
		terminalIdx := -1
		var terminalArgs string
		var regularIdxs []int
		for idx, tc := range msg.ToolCalls {
			if tc.Type != "function" {
				continue
			}
			if tc.Function.Name == terminal.Name {
				called := true
				if terminal.Parse != nil {
					var err error
					called, err = terminal.Parse(tc.Function.Arguments)
					if err != nil {
						messages = append(messages, openai.ToolMessage(terminal.ErrMsg(err), tc.ID))
						continue
					}
				}
				if !called {
					continue
				}
				terminalIdx = idx
				terminalArgs = tc.Function.Arguments
				break
			}
			regularIdxs = append(regularIdxs, idx)
		}

		// Pass 2: run all regular tool calls concurrently. Each goroutine
		// only writes to its own slot, so no locking is needed here; results
		// are appended to messages in original index order afterward, since
		// order doesn't matter to the API (each ToolMessage is matched by
		// tool_call_id, not position) but a stable order keeps logs/tests
		// deterministic.
		type toolOutcome struct {
			text string
			urls map[string]struct{}
		}
		outcomes := make([]toolOutcome, len(regularIdxs))
		var wg sync.WaitGroup
		for i, idx := range regularIdxs {
			wg.Add(1)
			go func(i, idx int) {
				defer wg.Done()
				tc := msg.ToolCalls[idx]
				logger.Debug("calling tool", zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))
				span.AddEvent("agent.tool_call", trace.WithAttributes(attribute.String("tool", tc.Function.Name)))

				toolRes, toolErr := toolSource.Call(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
				if toolErr != nil {
					logger.Warn("tool call failed", zap.String("tool", tc.Function.Name), zap.Error(toolErr))
					toolRes = fmt.Sprintf("error: %v", toolErr)
				}
				urls := make(map[string]struct{})
				collectURLs(urls, toolRes)
				outcomes[i] = toolOutcome{text: fenceToolResult(tag, toolRes), urls: urls}
			}(i, idx)
		}
		wg.Wait()

		for i, idx := range regularIdxs {
			tc := msg.ToolCalls[idx]
			res.ToolsUsed++
			for u := range outcomes[i].urls {
				res.DiscoveredURLs[u] = struct{}{}
			}
			messages = append(messages, openai.ToolMessage(outcomes[i].text, tc.ID))
		}

		var done bool
		if terminalIdx >= 0 {
			res.TerminalArgs = terminalArgs
			messages = append(messages, openai.ToolMessage(terminal.SuccessMsg, msg.ToolCalls[terminalIdx].ID))
			done = true
		}

		if done {
			res.Conversation = messages
			res.DurationMS = time.Since(start).Milliseconds()
			span.AddEvent("agent." + terminal.Name)
			return res, nil
		}
	}

	res.DurationMS = time.Since(start).Milliseconds()
	return res, &maxIterationsError{max: maxIterations}
}

// randomTag generates a short random hex tag used to delimit untrusted
// content blocks, so the delimiter itself can't be guessed/spoofed by
// injected content in a tool result.
func randomTag() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// fenceToolResult wraps a tool result in a random-tagged delimiter block so
// the model can visually distinguish untrusted tool output from its own
// instructions, mirroring buildSeedMessages' fencing of seed search results.
func fenceToolResult(tag, text string) string {
	return fmt.Sprintf("<<<TOOL_RESULT_%s>>>\n%s\n<<<END_TOOL_RESULT_%s>>>", tag, text, tag)
}

// collectURLs extracts URLs only from structured "source_url"/"url" JSON
// fields in a tool result, never from free-form body text (e.g. a chunk's
// text or a fetched page's body) — those are untrusted content and must not
// be treated as vetted source links (see filterButtons).
//
// It does this by actually decoding the result as JSON and walking the
// resulting value tree, rather than regexing the raw string: a regex over
// raw text would also match a "url": "..." substring that merely appears
// inside untrusted string content (a tool error message, ingested chunk
// text, or raw shell output from the ssh sandbox tools), letting a
// prompt-injected payload smuggle an attacker-controlled URL into the
// allowed set. json.Unmarshal only exposes object keys that are structurally
// keys, not text that happens to look like one inside a string value, and a
// non-JSON result (e.g. plain-text tool errors or shell output) simply
// yields no URLs instead of a false match.
func collectURLs(dst map[string]struct{}, text string) {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return
	}
	walkURLs(dst, v)
}

func walkURLs(dst map[string]struct{}, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "source_url" || k == "url" {
				if s, ok := val.(string); ok {
					addURL(dst, s)
					continue
				}
			}
			walkURLs(dst, val)
		}
	case []any:
		for _, e := range t {
			walkURLs(dst, e)
		}
	}
}

func addURL(dst map[string]struct{}, raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:!?)]}>")
	if raw == "" {
		return
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return
	}
	dst[raw] = struct{}{}
}
