package openrouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamError(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		code    int
		msgSub  string
	}{
		{
			name: "healthy",
			raw:  `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		},
		{
			name:    "top level error",
			raw:     `{"error":{"code":429,"message":"rate limited"}}`,
			wantErr: true, code: 429, msgSub: "rate limited",
		},
		{
			// The incident shape: HTTP 200, provider fails mid-generation, error
			// embedded in the choice alongside finish_reason "error".
			name:    "choice embedded error",
			raw:     `{"choices":[{"message":{"role":"assistant","content":"Connect timeout, please try again later."},"finish_reason":"error","error":{"code":502,"message":"Connect timeout, please try again later."}}]}`,
			wantErr: true, code: 502, msgSub: "Connect timeout",
		},
		{
			name:    "finish reason error without error object",
			raw:     `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"error"}]}`,
			wantErr: true, msgSub: "finish_reason",
		},
		{
			name:    "error object missing message",
			raw:     `{"error":{"code":500}}`,
			wantErr: true, code: 500, msgSub: "provider reported an error",
		},
		{name: "empty", raw: ""},
		{name: "not json", raw: "Connect timeout, please try again later."},
		{name: "unrelated json", raw: `{"choices":[{"message":{"content":"an error occurred, but this is normal prose"},"finish_reason":"stop"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamError(tt.raw)
			if !tt.wantErr {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.code, got.Code)
			require.Contains(t, got.Message, tt.msgSub)
			require.Contains(t, got.Error(), tt.msgSub)
		})
	}
}

func FuzzUpstreamError(f *testing.F) {
	f.Add("")
	f.Add(`{"error":{"code":429,"message":"rate limited"}}`)
	f.Add(`{"choices":[{"finish_reason":"error","error":{"code":502,"message":"boom"}}]}`)
	f.Add(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	f.Add(`{"error":{"code":"not-a-number","message":"x"}}`)
	f.Fuzz(func(t *testing.T, raw string) {
		// Must never panic and must return a usable error when non-nil.
		if e := upstreamError(raw); e != nil {
			_ = e.Error()
		}
	})
}
