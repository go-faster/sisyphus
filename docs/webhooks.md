# Webhooks

`ssapi` can accept GitLab and Jira webhooks to trigger near real-time ingestion. Webhooks are invalidation signals only: after receiving a valid webhook, `ssapi` debounces triggers and runs the same incremental GitLab/Jira ingestion logic used by `ssingest`.

No webhook events are stored. Periodic `ssingest` runs are still recommended as a backstop for missed deliveries or service restarts.

## Endpoints

- `POST /webhooks/gitlab`
- `POST /webhooks/jira`

With the default Docker Compose port mapping, use:

- `http://<host>:18079/webhooks/gitlab`
- `http://<host>:18079/webhooks/jira`

## Configuration

Enable webhooks under the provider configuration:

```yaml
gitlab:
  base_url: https://gitlab.example.com
  token:
    env: SISYPHUS_GITLAB_TOKEN
  projects:
    - ref: group/docs
  issues: true
  merge_requests: true
  releases: true
  webhook:
    enabled: true
    secret:
      env: SISYPHUS_GITLAB_WEBHOOK_SECRET

jira:
  base_url: https://jira.example.com
  email: bot@example.com
  api_token:
    env: SISYPHUS_JIRA_APITOKEN
  projects:
    - key: SUP
  webhook:
    enabled: true
    secret:
      env: SISYPHUS_JIRA_WEBHOOK_SECRET
```

Set secrets in the service environment:

```bash
export SISYPHUS_GITLAB_WEBHOOK_SECRET='change-me-gitlab'
export SISYPHUS_JIRA_WEBHOOK_SECRET='change-me-jira'
```

The handlers are only mounted when both `webhook.enabled: true` and a non-empty secret are configured.

## GitLab Setup

In GitLab project settings, add a project webhook:

- URL: `https://<public-ssapi-host>/webhooks/gitlab`
- Secret token: value of `SISYPHUS_GITLAB_WEBHOOK_SECRET`
- Events: Issue events, Merge request events, Release events as needed.

`ssapi` validates `X-Gitlab-Token`. Any valid GitLab webhook triggers GitLab ingestion for all enabled GitLab resource types in the config. It does not rely on the webhook payload as source-of-truth.

## Jira Setup

Configure Jira or an ingress/proxy in front of `ssapi` to send:

```http
X-Jira-Token: <SISYPHUS_JIRA_WEBHOOK_SECRET>
```

Use URL:

```text
https://<public-ssapi-host>/webhooks/jira
```

Recommended Jira events:

- `jira:issue_created`
- `jira:issue_updated`
- `jira:issue_deleted`

Current behavior is source-level refresh: any valid Jira webhook triggers incremental Jira ingestion for configured Jira projects.

## Debounce Behavior

Webhook triggers are debounced for 10 seconds per provider. Multiple webhooks during the debounce window coalesce into one ingestion run. If a webhook arrives while ingestion is already running, `ssapi` marks the provider dirty and runs ingestion once more after the current run finishes.

## Reliability Model

- Accepted webhooks return `202 Accepted` after authentication and trigger enqueueing.
- Triggers are in-memory, not persisted.
- If `ssapi` restarts after accepting a webhook but before ingestion runs, that trigger can be lost.
- Periodic `ssingest gitlab` and `ssingest jira` runs should remain scheduled if missed updates are unacceptable.

## Manual Test

GitLab handler:

```bash
curl -i -X POST http://localhost:18079/webhooks/gitlab \
  -H "X-Gitlab-Token: ${SISYPHUS_GITLAB_WEBHOOK_SECRET}" \
  -H 'Content-Type: application/json' \
  -d '{}'
```

Jira handler:

```bash
curl -i -X POST http://localhost:18079/webhooks/jira \
  -H "X-Jira-Token: ${SISYPHUS_JIRA_WEBHOOK_SECRET}" \
  -H 'Content-Type: application/json' \
  -d '{}'
```

Expected response:

```text
HTTP/1.1 202 Accepted
```
