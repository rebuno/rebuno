<p align="center">
  <a href="https://rebuno.io"><img src="rebuno.svg" alt="rebuno" width="200"></a>
</p>

<p align="center">
  <a href="https://discord.gg/zv72f2PvzB"><img src="https://img.shields.io/discord/1483512352438616238?logo=discord&logoColor=white&color=5865F2&label=Discord&style=for-the-badge" alt="Discord"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?&style=for-the-badge" alt="License"></a>
</p>

An open-source runtime for autonomous agents.

Rebuno gives your agents durable execution, an event-sourced record of everything they did, and optional governance over what they're allowed to do.

## Quick Start

**Prerequisites:** Go 1.25+, Python 3.10+

Start the kernel:

```bash
go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml
```

Start an agent:

```bash
pip install rebuno
python examples/python/hello.py
```

Create an execution:

```bash
exec create hello {"query": "hello world"}
```

See the full audit trail with `exec events {id}`.

## License

[MIT](LICENSE)
