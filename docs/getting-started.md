# Getting started

## Before you begin

Install Go 1.26+, plus Python 3.10+ or Node 22+ to run the agent.

## Start the dev kernel

Point the dev kernel at a config to register agents on boot:

```bash
go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml
```

Each entry gives an agent's `webhook_url`, its HMAC `secret`, and optionally a
`policy` block or a `policy_file` path. Without a policy, an agent runs
unrestricted.

```yaml
agents:
  - id: hello
    webhook_url: http://localhost:5000/webhook
    secret: hello-secret
```

## Run an agent

An agent is an HTTP service that speaks the kernel's [protocol](agents.md). The
`hello` example listens on `:5000` and calls back to the kernel on `:8080`. In a
second terminal, start it in Python:

```bash
pip install rebuno
python examples/python/hello.py
```

Or in TypeScript:

```bash
npm install rebuno
npx tsx examples/typescript/hello.ts
```

## Create an execution

In a third terminal:

```bash
rebuno exec create hello '{"query": "hello world"}'
  created 01a05eb7-e102-78e9 (pending); follow with 'rebuno exec watch 01a05eb7-e102-78e9'
rebuno exec watch 01a05eb7-e102-78e9
rebuno exec events 01a05eb7-e102-78e9
```

`exec watch` tails events until the run finishes. `exec events` prints the whole
log with payloads. See [CLI](cli.md).

## Where to go next

- [Architecture](architecture.md)
- [Agents](agents.md)
- [Policy](policy.md)
- [HTTP API](api.md)
- [Python SDK](sdk/python)
- [TypeScript SDK](sdk/typescript)
- [Deployment](deployment.md)
