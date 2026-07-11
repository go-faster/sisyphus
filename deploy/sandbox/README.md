# Sandbox Container

This image runs the read-only SSH sandbox used by the agentic `/context` flow.

## Build

```bash
docker build -t sisyphus-sandbox ./deploy/sandbox
```

## Host key

The sandbox host key is generated on first boot unless you bake one into the
image. Add the runtime key to the client known_hosts file once:

```bash
ssh-keyscan -p 2222 localhost >> deploy/ssh/known_hosts
```

## Security model

- root login is disabled
- password auth is disabled
- `/repos` is mounted read-only
- `/tmp` is tmpfs at runtime
- the container drops all Linux capabilities
- commands are expected to be read-only and constrained by `ssh-mcp`
