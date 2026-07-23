# sisyphus Helm chart

Kubernetes deployment of the whole sisyphus stack — the k8s counterpart of
`deploy/docker-compose.yml`.

```
helm upgrade --install sisyphus deploy/helm/sisyphus \
  -n sisyphus --create-namespace \
  -f my-values.yaml
```

See `sisyphus/values-example.yaml` for a full, realistic deployment (corporate
GitLab + Jira behind a SOCKS proxy, a client Grafana with several VictoriaLogs
datasources, Telegram bot, SSH sandbox).

## What it deploys

| Group | Objects |
|---|---|
| datastores | `postgres`, `qdrant`, `ollama` StatefulSets (+ a Job that pulls the embedding model) |
| app | `ssapi`, `ssingest`, `ssbot`, `ssagent`, `ssmcp` |
| mcp | `mcpgateway` + one Deployment per entry in `mcp.servers` (VictoriaLogs, Grafana, Jira, …) |
| sandbox | `sandbox` (+ `ssh-mcp` in ssh mode), egress-denied by a NetworkPolicy |
| telemetry | `otelcol`; optional `alertmanager` + `vmalert` |

Every datastore can be swapped for an existing one (`postgres.enabled: false` +
`postgres.external.dsn`, and the same for `qdrant`/`ollama`).

## Config

`config` in values **is** the app's `config.yaml`. The chart computes the parts
that depend on cluster DNS (database DSN, qdrant/ollama endpoints, listen
addresses, `api.base_url`, `agent.base_url`, `context.gateway_url`) and merges
your `config` on top, so you only write what is actually yours: repos, GitLab and
Jira projects, models, allowed Telegram chats.

## Secrets

Either set `secrets.values.*` (chart renders a Secret) or pre-create a Secret with
the same keys and set `secrets.existingSecret`. Keys are the env var names the app
reads: `SISYPHUS_API_AUTH_TOKEN`, `SISYPHUS_AGENT_AUTH_TOKEN`,
`SISYPHUS_MCP_AUTH_TOKEN`, `SISYPHUS_OPENROUTER_API_KEY`, `SISYPHUS_GITLAB_TOKEN`,
`SISYPHUS_JIRA_PASSWORD`, `SISYPHUS_TELEGRAM_APP_HASH`,
`SISYPHUS_TELEGRAM_BOT_TOKEN`, `GRAFANA_SERVICE_ACCOUNT_TOKEN`, plus the webhook
secrets.

The three bearer tokens the app itself checks —  `SISYPHUS_API_AUTH_TOKEN`,
`SISYPHUS_AGENT_AUTH_TOKEN`, `SISYPHUS_MCP_AUTH_TOKEN` — are random-generated at
install time if left blank, and pinned to their existing value on every upgrade
(via `lookup`, so it never rotates a token a client is still using). Everything
else (GitLab/Jira/Telegram/OpenRouter/Grafana creds) has nothing to generate —
those come from you. With `existingSecret` none of this applies: the chart cannot
derive the database DSN either — the Secret must also carry
`SISYPHUS_DATABASE_DSN` and, if the chart runs postgres, `POSTGRES_PASSWORD`.

## Adding an MCP server (VictoriaLogs, Grafana, anything)

Add an entry under `mcp.servers`. It becomes a Deployment, a Service and a
`[[upstream]]` in `gateway.toml` — no template edits:

```yaml
mcp:
  servers:
    vmlogs-prod:
      enabled: true
      image: ghcr.io/victoriametrics/mcp-victorialogs:v1.9.0
      port: 8000
      proxy: true              # route egress through proxy.url
      probe: http
      probePath: /health/liveness
      env:
        MCP_SERVER_MODE: http
        MCP_LISTEN_ADDR: ":8000"
        VL_INSTANCE_ENTRYPOINT: https://grafana.example.com/api/datasources/proxy/uid/<uid>
        VL_DEFAULT_TENANT_ID: "1:1"     # upstream default 0:0 returns nothing here
        VL_INSTANCE_HEADERS: "Authorization=Bearer $(GRAFANA_SERVICE_ACCOUNT_TOKEN)"
      secretEnv:
        GRAFANA_SERVICE_ACCOUNT_TOKEN: GRAFANA_SERVICE_ACCOUNT_TOKEN
      gateway:
        prefix: "vmlogs_prod."
        allow: ["*"]
```

`secretEnv` vars are emitted before `env`, so `env` values can reference them with
`$(VAR)` (kubelet expands it).

### stdio upstreams (glab)

`mcp.stdioUpstreams.gitlab` makes the gateway exec `glab mcp serve` **inside its
own container**, so the `glab` binary must be in the gateway image. Build one:

```dockerfile
ARG GLAB_VERSION=v1.107.0
ARG MCPGATEWAY_VERSION=0.4.1
FROM gitlab/glab:${GLAB_VERSION} AS glab
FROM ghcr.io/go-faster/gooners/mcpgateway:${MCPGATEWAY_VERSION}
COPY --from=glab /usr/bin/glab /usr/local/bin/glab
```

and point `mcp.gateway.image` at it. The default allow-list is read-only; never
add `glab_api` (raw HTTP passthrough, any method).

## Sandbox

`sandbox.mode: ssh` (today): the sandbox runs `sshd`, and a separate `ssh-mcp` pod
turns SSH into MCP tools. The chart generates ssh-mcp's `~/.ssh/config` with a
`Host` alias equal to `config.context.sandbox_machine` pointing at the sandbox
Service, so the agent's `ssh_*` tools keep working unchanged.

`sandbox.mode: mcp` (later): when the sandbox image speaks MCP itself, flip the
mode and set `sandbox.mcp.image`/`port`/`path`. `ssh-mcp` is no longer deployed,
the gateway's `sandbox` upstream points straight at the sandbox Service, and the
NetworkPolicy follows. Nothing else changes.

### Keys (ssh mode)

Leave `sandbox.ssh.*` untouched and the chart generates a keypair for you —
`sshKeygenJob` (on by default) runs a pre-install/upgrade Job that checks the
`<release>-sandbox-ssh` Secret via the K8s API and, if it's not there yet, runs
`ssh-keygen` and creates it. Idempotent: it never touches an existing Secret, so
upgrades don't rotate a keypair a running deployment is still using. The Secret
it creates isn't part of the Helm release (it's created directly via the API, not
templated) so `helm uninstall` leaves it behind on purpose — delete it yourself if
you want a fresh keypair next install. Set `sshKeygenJob.enabled: false` to turn
this off and fall back to the old fail-fast behavior instead.

To bring your own keypair instead, pass the files, don't paste them:

```
--set-file sandbox.ssh.privateKey=ssh/id_ed25519 \
--set-file sandbox.ssh.hostKey=ssh/ssh_host_ed25519_key \
--set-file sandbox.ssh.authorizedKeys=ssh/id_ed25519.pub \
--set-file sandbox.ssh.hostKeyPub=ssh/ssh_host_ed25519_key.pub
```

`hostKeyPub` is what turns on `StrictHostKeyChecking yes` (the chart builds
`known_hosts` from it). Without it the client accepts any host key — including
the auto-generated keypair above, which never sets `hostKeyPub` for the same
chart-render-vs-Job-runtime-ordering reason it can't inject its own known_hosts
line either.

The sandbox image is built from `deploy/sandbox` — push it somewhere and set
`sandbox.image`.

### Isolation

The sandbox executes shell commands an LLM chose while reading untrusted ingested
content. Its NetworkPolicy denies **all** egress and allows ingress only from its
MCP front-end; the pod gets a read-only rootfs, tmpfs-only writable paths and no
service-account token. This only holds if your CNI enforces NetworkPolicy
(calico/cilium do; plain flannel does not) — if it does not, the sandbox has full
network access.

## Storage

`git-workdir` is written by `ssingest` and read by `ssapi` (file-content tool) and
the sandbox (`/repos`), so it defaults to a **ReadWriteMany** PVC. On a cluster
without RWX set `gitWorkdir.shared: false`: only `ssingest` mounts it, `ssapi`
falls back to reading document bodies from Postgres, and the sandbox has no repos.

## Ops notes

- Schema migrations run once via the `migrateJob` pre-install/pre-upgrade hook Job
  (`ssapi migrate`), before any Deployment is created or updated — no serving
  replica migrates itself, so `ssapi` can run N replicas. `ssingest` waits for
  `ssapi`'s `/ready` in an init container, which also fails while any migration
  is still pending.
- `ssingest` and `ssbot` are pinned to one replica with a `Recreate` strategy: two
  ingestion schedulers would race on the same source rows, two bots would
  double-answer every Telegram update.
- Indexing scales separately from fetching. `ssingest serve` publishes one index
  job per changed document; setting `ssingestWorker.enabled: true` deploys
  `ssingest worker` replicas to consume them and flips `config.ingest.worker.enabled`
  to `false` so the scheduler pod stops competing for embedding capacity. Workers
  mount no PVC and hold no source credentials — every document arrives whole in
  its job — so `ssingestWorker.replicas` is the knob for ingestion throughput.
  Left disabled, `ssingest serve` indexes what it publishes in-process, which is
  the right shape for a small install.
- The bot fails closed — with `config.telegram.allowed_chats` /
  `allowed_user_ids` empty it silently ignores every message.
