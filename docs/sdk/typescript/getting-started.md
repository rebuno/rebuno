# Getting started

## Install

```bash
npm install rebuno
```

Requires Node 18+. The SDK is ESM-only and has no runtime dependencies — it uses
the platform's `fetch`, Web Crypto, `AsyncLocalStorage`, and `node:http` to both
host an agent and act as a client. Bring your own LLM framework (e.g. the Vercel
AI SDK) when you need one.

## Configuration

Every entry point reads from constructor options first and falls back to
environment variables, so the same code runs locally and in production without
edits:

| Variable | Used by | Purpose |
|----------|---------|---------|
| `REBUNO_URL` | `Agent`, `Client` | kernel base URL |
| `REBUNO_AGENT_SECRET` | `Agent` | HMAC secret shared with the kernel; signs every request and verifies inbound webhooks |
| `REBUNO_API_KEY` | `Client` | Bearer token for client/admin routes |

```ts
// explicit
const agent = new Agent("dev-agent", { secret: "dev-secret", kernelUrl: "http://localhost:8080" });

// from the environment (REBUNO_URL + REBUNO_AGENT_SECRET)
const agent = new Agent("dev-agent");
```

## The loop

There are two processes, and they talk to each other only through the kernel.

```
  your backend                 kernel                    your agent process
 ┌────────────┐          ┌──────────────┐              ┌──────────────────┐
 │  Client    │  create  │              │   webhook    │  agent.serve()   │
 │  .create() │ ───────► │  executions  │ ───────────► │  → your handler  │
 │            │          │  + steps     │ ◄─────────── │  → defineTool /   │
 │  .get()    │ ◄─────── │  (durable)   │  submit_step │    rebunoFetch /  │
 └────────────┘  status  └──────────────┘   complete   │    step()        │
                                                        └──────────────────┘
```

1. A **client** calls `client.create(agentId, input)`. The kernel records a new
   execution and dispatches it by POSTing a signed webhook to your agent.
2. Your **agent** verifies the signature, looks up the execution, and runs your
   handler. Each effect the handler performs is submitted to the kernel as a
   step *before* it runs — the kernel decides whether it proceeds, replays a
   recorded result, is denied by policy, or must wait for approval.
3. When the handler returns, the agent reports the output and the execution
   completes. If the handler blocked on an approval or crashed, the kernel
   re-dispatches later and the handler **replays** its recorded steps to get
   back to where it left off.

## A complete example

The agent process — hosts the handler and records its effects:

```ts
// agent.ts
import { Agent, defineTool } from "rebuno";

const search = defineTool({
  name: "search",
  description: "Search the web",
  inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] },
  execute: async ({ query }: { query: string }) => [`result for ${query}`],
});

async function process(input: { prompt: string }) {
  const hits = await search.execute({ query: input.prompt });
  return { answer: hits };
}

const agent = new Agent("dev-agent", { secret: "dev-secret", kernelUrl: "http://localhost:8080" });
await agent.serve({ port: 5000 }, process); // blocks, serving the webhook
```

A client that kicks off an execution and reads the result:

```ts
// client.ts
import { Client } from "rebuno";

const client = new Client({ baseUrl: "http://localhost:8080" });
const ex = await client.create("dev-agent", { prompt: "hello" });
console.log(await client.get(ex.id));
```

## Running locally

Run the agent in one terminal (it blocks, serving the webhook), then create an
execution from another:

```bash
tsx agent.ts      # terminal 1
tsx client.ts     # terminal 2
```

The kernel itself is a separate service — point `REBUNO_URL` / `kernelUrl` at
wherever it runs. Next: [Agents](agents.md).
