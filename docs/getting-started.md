# Getting Started

## Quick Start

**Prerequisites:** Go 1.25+, Python 3.10+

### 1. Start the kernel

```bash
go install ./cmd/rebuno
rebuno dev
```

You should see:

```
rebuno dev — development mode

  kernel    http://127.0.0.1:8080
  policy    permissive (all tools allowed)
  storage   in-memory (data lost on restart)

  Waiting for agents...
```

### 2. Start the hello world agent
```bash
pip install rebuno
python examples/agent/hello.py
```

The agent connects to the kernel via SSE and waits for executions.

### 3. Create an execution
```bash
rebuno create --agent hello --input '{"query": "hello world"}'
```

Or with curl:

```bash
curl -s -X POST http://localhost:8080/v0/executions \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "hello", "input": {"query": "hello world"}}' | jq
```

The kernel assigns the execution to the agent. The agent proposes two tool calls (`reverse` and `word_count`) as intents, the kernel approves each one, and the agent returns the result:

```json
{
    "query": "hello world",
    "reversed": "dlrow olleh",
    "word_count": 2
}
```

### 4. View the event log

Every action is recorded as an immutable event:

```bash
rebuno events {id}
```

Or with curl:

```bash
curl -s http://localhost:8080/v0/executions/{id}/events | jq
```

Replace `{id}` with the execution ID from step 3. You'll see events like `execution.created`, `intent.accepted`, `step.created`, `step.completed`, and `execution.completed` — a full audit trail of what the agent did and what the kernel decided.

You can also follow events in real time with `rebuno events --tail {id}`.

### 5. Add a policy

Restart the kernel with a policy to see what happens when tools are denied:

```bash
rebuno dev --policy examples/policies/hello.yaml
```

Now `reverse` and `word_count` are allowed, but any other tool will be denied. To see a denial, you can modify the hello agent to call an unlisted tool — the kernel will reject the intent and the agent will receive a `PolicyError`.

### Add the Explorer

**Requires:** Node.js 18+

To also start the web-based execution viewer:

```bash
cd explorer
npm install
npm run dev
```

The explorer is available at `http://localhost:3000`. It connects to the kernel at `http://localhost:8080` by default. It shows executions, event timelines, and step details.

## Next Steps

- [Architecture](architecture.md) — Core concepts, workflow, and state transitions
- [Deployment](deployment.md) — Production setup, authentication, and configuration reference
- [Python SDK](sdk/python.md) — Building agents and runners in Python
- [TypeScript SDK](sdk/typescript.md) — Building agents and runners in TypeScript
- [Policy](policy.md) — Declarative policy rules
- [CLI](cli.md) — CLI reference
