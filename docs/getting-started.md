# Getting Started

**Prerequisites:** Go 1.25+, Python 3.10+

## 1. Start the dev kernel

The dev kernel runs entirely in memory — no Postgres, no auth, no external
dependencies. Point it at a provisioning manifest to register agents and their
policies on boot:

```bash
go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml
```

The manifest (`examples/rebuno.dev.yaml`) registers a few example agents. Each
entry gives the agent's `webhook_url`, its HMAC `secret`, and an optional policy:

```yaml
agents:
  - id: hello
    webhook_url: http://localhost:5000/webhook
    secret: hello-secret
    # no policy → permissive (allow all) in dev
```

When stdin is a terminal, the dev kernel drops you into an interactive REPL
(`rebuno>`). See [CLI](cli.md) for the commands.

## 2. Run an agent

An agent is any HTTP service that speaks the kernel's [protocol](agents.md). This
quickstart uses the `hello` example built with the [Python SDK](sdk/python.md); it
listens on the webhook URL the manifest registered (`:5000`) and talks back to the
kernel at `:8080`:

```bash
pip install rebuno
python examples/python/hello.py
```

See [`examples/python/hello.py`](../examples/python/hello.py) for the source, and
the [Python SDK](sdk/python.md) doc for how it's built.

## 3. Create an execution

From the kernel's REPL:

```
rebuno> exec create hello {"query": "hello world"}
  created a1b2c3d4 (pending) — 'exec watch a1b2c3d4' to follow
```

Watch it run, then inspect the full event log:

```
rebuno> exec watch a1b2c3d4
rebuno> exec events a1b2c3d4
```

You can also drive the kernel over HTTP, or use the interactive client which
streams events and resolves any human-in-the-loop approvals:

```bash
python examples/python/client.py --agent hello
```

## Where to go next

- [Architecture](architecture.md) — how durability and replay work.
- [Agents](agents.md) — the webhook protocol and dispatch/replay lifecycle.
- [Tools & effects](tools.md) — step identity and idempotency modes.
- [Policy](policy.md) — gate tool and LLM calls; require human approval.
- [Python SDK](sdk/python.md) — build an agent in Python.
- [Deployment](deployment.md) — run the production (Postgres-backed) kernel.
- [Dashboard](https://github.com/rebuno/dashboard) — web UI to view executions, steps, events, and agent activity.
