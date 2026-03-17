<p align="center">
  <a href="https://rebuno.io"><img src="rebuno.svg" alt="rebuno" width="200"></a>
</p>

<p align="center">
  <a href="https://discord.gg/zv72f2PvzB"><img src="https://img.shields.io/discord/1483512352438616238?logo=discord&logoColor=white&color=5865F2&label=Discord&style=for-the-badge" alt="Discord"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?&style=for-the-badge" alt="License"></a>
</p>

A kernel-authoritative execution runtime for AI agents.

Rebuno sits between your agents and the tools they use, giving you policy control, a complete audit trail, and operational visibility over autonomous agent behavior. Agents propose actions. The kernel decides whether they're allowed. Every decision is recorded.

## Quick Start

**Prerequisites:** Go 1.25+, Python 3.10+

Start the kernel:

```bash
go install ./cmd/rebuno
rebuno dev
```

Start an agent:

```bash
pip install rebuno
python examples/agent/hello.py
```

Create an execution:

```bash
rebuno create --agent hello --input '{"query": "hello world"}'
```

See the full audit trail with `rebuno events {id}`.

See [Getting Started](docs/getting-started.md) for the full walkthrough.

## Documentation

| Doc | Description |
|---|---|
| [Getting Started](docs/getting-started.md) | Quick start walkthrough |
| [Architecture](docs/architecture.md) | Core concepts and state transitions |
| [Deployment](docs/deployment.md) | Production setup, auth, and configuration |
| [Python SDK](docs/sdk.md) | Building agents and runners |
| [Tools](docs/tools.md) | Local, remote, and MCP tools |
| [Policy](docs/policy.md) | Declarative policy rules |
| [API Reference](docs/api.md) | HTTP endpoints and schemas |
| [Events](docs/events.md) | Event types and payloads |
| [CLI](docs/cli.md) | CLI reference |

## License

[MIT](LICENSE)
