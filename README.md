<p align="center">
  <a href="https://rebuno.io"><img src="rebuno.svg" alt="rebuno" width="200"></a>
</p>

<p align="center">
  <a href="https://discord.gg/zv72f2PvzB"><img src="https://img.shields.io/discord/1483512352438616238?logo=discord&logoColor=white&color=5865F2&label=Discord&style=for-the-badge" alt="Discord"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?&style=for-the-badge" alt="License"></a>
</p>

An open-source execution runtime for production agents.

Rebuno gives your agents durable execution (crash and resume without re-running side effects), an event-sourced record of everything they did, and optional governance over what they're allowed to do.

## Quick Start

**Prerequisites:** Go 1.25+, Python 3.10+

Start the dev kernel (in-memory, no dependencies). With a terminal attached it drops you into a REPL:

```bash
go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml
```

Start an agent in another terminal:

```bash
pip install rebuno
python examples/python/hello.py
```

Create an execution from the REPL and follow its event log:

```
rebuno> exec create hello {"query": "hello world"}
rebuno> exec events <id>
```

## Documentation

Start here:

- [Getting Started](docs/getting-started.md) — run the kernel and your first agent.
- [Architecture](docs/architecture.md) — the domain model, state machines, and how durability works.

Reference:

- [Agents](docs/agents.md) — how an agent process receives work and drives its effects.
- [Tools](docs/tools.md) — tools, step identity, and idempotency.
- [LLM calls](docs/llm-calls.md) — intercepting LLM requests so they replay durably.
- [Policy](docs/policy.md) — the YAML rule language for allow / deny / require-approval.
- [Events](docs/events.md) — the event taxonomy and payloads.
- [HTTP API](docs/api.md) — the `/v0` client, agent, and admin endpoints.
- [CLI](docs/cli.md) — the built-in `rebuno` REPL.
- [Deployment](docs/deployment.md) — running the production kernel, config, and Docker.
- [Python SDK](docs/sdk/python) — building with Python
- [TypeScript SDK](docs/sdk/typescript) — building with TypeScript

## License

[MIT](LICENSE)
