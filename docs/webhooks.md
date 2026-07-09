# Webhooks and Polling

`ssapi` can accept GitLab and Jira webhooks to trigger near real-time ingestion, and/or poll on a timer. Both mechanisms feed the same debounced `Trigger`, so a poll tick racing a webhook just coalesces into one run instead of running ingestion twice. Webhooks and polling are invalidation signals only: they run the same incremental GitLab/Jira ingestion logic used by `ssingest`.

No webhook events are stored. Periodic `ssingest` runs are still recommended as a backstop for missed deliveries or service restarts, unless polling is enabled (see below), in which case it already provides that backstop.

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
- Periodic `ssingest gitlab` and `ssingest jira` runs should remain scheduled if missed updates are unacceptable, unless polling (below) is enabled instead.

## Polling

Each provider can additionally run incremental ingestion on a timer, independent of webhooks:

```yaml
gitlab:
  poll:
    interval_seconds: 300

jira:
  poll:
    interval_seconds: 300
```

`interval_seconds <= 0` (or omitting `poll`) disables polling for that provider. When enabled, `ssapi` fires the same debounced trigger used by webhooks once at startup and then every `interval_seconds`, so ingestion still runs even if webhooks are unset, misconfigured, or dropped. Polling only requires the provider's normal ingestion config (`base_url`, token, `projects`, etc.) — it does not require `webhook.enabled` or a secret.

Unlike the source-level webhook triggers, a poll tick doesn't imply anything changed; it just re-runs the same incremental fetch (bounded by each provider's sync cursor), so it costs one "no changes" API round trip per interval when nothing changed upstream.

## Metrics

`internal/webhook` emits OTel metrics (meter `github.com/go-faster/sisyphus/webhook`):

- `sisyphus.webhook.requests` (counter, `provider`, `result=accepted|unauthorized`) — incoming webhook HTTP requests.
- `sisyphus.webhook.trigger.fires` (counter, `key`) — `Trigger.Fire` calls, from webhooks or polling.
- `sisyphus.webhook.trigger.runs` (counter, `key`, `status=ok|error`) — debounced ingestion runs actually executed.
- `sisyphus.webhook.trigger.run.duration` (histogram, seconds, `key`, `status`) — duration of each ingestion run.
- `sisyphus.webhook.poll.ticks` (counter, `key`) — poller ticks fired (including the immediate startup fire).

`key`/`provider` is `gitlab` or `jira`.

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
