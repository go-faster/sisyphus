package webhook

import (
	"crypto/subtle"
	"net/http"
)

const (
	gitlabHeader = "X-Gitlab-Token"
	jiraHeader   = "X-Jira-Token"
)

// NewGitLabHandler returns an http.Handler that validates the GitLab webhook
// secret from the X-Gitlab-Token header, fires the "gitlab" trigger, and responds
// 202 Accepted. If secret is empty, the handler skips validation (useful for
// testing) and logs a warning at construction time.
func NewGitLabHandler(secret string, trigger *Trigger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret != "" {
			token := r.Header.Get(gitlabHeader)
			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		trigger.Fire("gitlab")
		w.WriteHeader(http.StatusAccepted)
	})
}

// NewJiraHandler returns an http.Handler that validates the Jira webhook
// secret from the X-Jira-Token header, fires the "jira" trigger, and responds
// 202 Accepted.
func NewJiraHandler(secret string, trigger *Trigger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret != "" {
			token := r.Header.Get(jiraHeader)
			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		trigger.Fire("jira")
		w.WriteHeader(http.StatusAccepted)
	})
}
