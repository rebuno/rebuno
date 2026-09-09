<p align="center">
  <a href="https://rebuno.io">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/rebuno-dark.svg">
      <source media="(prefers-color-scheme: light)" srcset="docs/assets/brand/rebuno-light.svg">
      <img src="docs/assets/brand/rebuno-dark.svg" alt="rebuno" width="280">
    </picture>
  </a>
</p>

Rebuno is an open-source execution runtime for production agents.

It records every tool call and LLM call as a durable step. Interrupted runs resume from the last recorded step instead of starting over. Any step can be allowed, denied, or held for human approval.

<p align="center">
  <img src="docs/assets/execution.gif" width="900">
</p>

## Quick Start

**Prerequisites:** Go 1.26+, Python 3.11+ / Node 22+

Start the dev kernel:

```bash
go run ./cmd/rebuno dev --config examples/rebuno.dev.yaml
```

Start an agent in another terminal:

Python
```bash
pip install rebuno
python examples/python/hello.py
```

TypeScript
```bash
npm install rebuno
npx tsx examples/typescript/hello.ts
```

Create an execution and follow its event log:

```bash
rebuno exec create hello '{"query": "hello world"}'
rebuno exec watch <id>
```

## Documentation

Start here:

- [Quickstart](docs/getting-started.mdx): run the kernel and your first agent.
- [Architecture](docs/architecture.mdx): the domain model, state machines, and how durability works.

Reference:

- [Agents](docs/agents.mdx): how an agent process receives work and drives its effects.
- [Tools](docs/tools.mdx): effects, step identity, and idempotency.
- [LLM calls](docs/llm-calls.mdx): intercepting LLM requests so they replay durably.
- [Streaming](docs/streaming.mdx): live token deltas while a step is running.
- [Policy](docs/policy.mdx): the YAML rule language for allow / deny / require-approval.
- [Events](docs/events.mdx): the event types and their payloads.
- [HTTP API](docs/api.mdx): the kernel's HTTP endpoints under `/v0`.
- [CLI](docs/cli.mdx): the `rebuno` binary and its commands.
- [Deployment](docs/deployment.mdx): running the production kernel, config, and Docker.
- [Python SDK](docs/sdk/python/overview.mdx): building with Python
- [TypeScript SDK](docs/sdk/typescript/overview.mdx): building with TypeScript
- [Dashboard](docs/dashboard.mdx): web UI to view executions, steps, events, and agent activity.

## License

[MIT](LICENSE)
