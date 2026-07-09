package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitLabHandlerAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		wantStatus int
		wantFire   bool
	}{
		{
			name:       "missing token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong token",
			header:     "wrong",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid token",
			header:     "secret",
			wantStatus: http.StatusAccepted,
			wantFire:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fired := make(chan struct{}, 1)
			trigger := NewTrigger(t.Context(), TriggerOptions{
				Window: time.Nanosecond,
			})
			trigger.Register("gitlab", func(_ context.Context) error {
				fired <- struct{}{}
				return nil
			})
			defer trigger.Wait()

			req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", http.NoBody)
			if tt.header != "" {
				req.Header.Set(gitlabHeader, tt.header)
			}
			rr := httptest.NewRecorder()

			NewGitLabHandler("secret", trigger).ServeHTTP(rr, req)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}

			if tt.wantFire {
				select {
				case <-fired:
				case <-time.After(time.Second):
					t.Fatal("trigger was not fired")
				}
				return
			}

			select {
			case <-fired:
				t.Fatal("trigger fired unexpectedly")
			case <-time.After(10 * time.Millisecond):
			}
		})
	}
}

func TestJiraHandlerAuth(t *testing.T) {
	t.Parallel()

	fired := make(chan struct{}, 1)
	trigger := NewTrigger(t.Context(), TriggerOptions{
		Window: time.Nanosecond,
	})
	trigger.Register("jira", func(_ context.Context) error {
		fired <- struct{}{}
		return nil
	})
	defer trigger.Wait()

	req := httptest.NewRequest(http.MethodPost, "/webhooks/jira", http.NoBody)
	req.Header.Set(jiraHeader, "secret")
	rr := httptest.NewRecorder()

	NewJiraHandler("secret", trigger).ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("trigger was not fired")
	}
}
