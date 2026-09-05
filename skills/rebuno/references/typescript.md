# TypeScript

Requires Node 22+ and ESM. Add `rebuno` with the project's package manager
(e.g. `npm install rebuno`); reuse its runner/build configuration.
A new project can add `tsx`, `typescript`, and `@types/node` as dev dependencies.

## Starter

With the setup guide's environment and registration, save as `agent.mts` and
run with the installed `tsx` (`npx tsx agent.mts`):

```ts
import { Agent, defineTool } from "rebuno";

const wordCount = defineTool({
  name: "word_count",
  execute: async ({ text }: { text: string }) => ({
    count: text.split(/\s+/).filter(Boolean).length,
  }),
});

async function handle(input: { query: string }) {
  return { result: await wordCount({ text: input.query }) };
}

await new Agent("my-agent").serve({ host: "127.0.0.1", port: 5000 }, handle);
```

This starter records a tool call; add the user's actual model loop when requested.
The handler takes one input object and returns JSON data. `Agent`'s optional
`inputSchema` uses Standard Schema validation; type annotations do not validate JSON.

## Existing applications

- **Server:** `agent.bind(handle)` attaches the handler; `agent.fetch` accepts
  a Web `Request` and returns a `Response`. Adapt Express/framework objects;
  preserve original body bytes and signature headers. Include mount prefixes
  in registration. Hosting needs Node-compatible facilities and must keep work
  alive after webhook acknowledgment.
- **Functions:** `defineTool({ name: "search", execute: existingSearch })`
  wraps a function taking one argument object. Both the returned callable and
  its `.execute` route through Rebuno. Use it in the framework's execution
  callback while retaining its schema. For non-idempotent writes add
  `idempotency: "at_most_once"`.
- **Tool objects:** `wrapTool({ name, invoke, inputSchema })` adapts an API.
  `toResult` serializes output; `transformArgs` changes arguments before both
  policy evaluation and invocation.
- **MCP:** `wrapMcpTools(descriptors, { call, prefix })` uses a
  `call(toolName, args)` adapter and records targets `<prefix>_<name>`.
  See [tool adapters](https://github.com/rebuno/rebuno/blob/main/docs/sdk/typescript/tools.md)
  for schema and session details.

## Model interception

Configure the provider actually used inside the bound handler, preserving its
existing options. For an AI SDK OpenAI provider:

```ts
import { createOpenAI } from "@ai-sdk/openai";
import { rebunoFetch } from "rebuno";

const openai = createOpenAI({ fetch: rebunoFetch });
```

Preserve a custom fetch with `createRebunoFetch({ fetch: existingFetch })`.
Interception records string JSON bodies using `model` as the target; other
bodies and calls outside an execution pass through without recording.
For streaming and transport details, consult
[LLM calls](https://github.com/rebuno/rebuno/blob/main/docs/sdk/typescript/llm-calls.md).

## Runtime details

- Tools and `step()` require an active dispatch. Tool denial returns a string
  even when the declared result type is an object; denied `step()` throws
  `PolicyError`.
- At broad provider/framework catch boundaries, call `raiseForRefusal(error)`
  from `rebuno` before retry/fallback logic. Let `Blocked` and `Terminated` unwind.
- `step(name, fn, args?, idempotency?)` passes the whole argument object to `fn`.
  After importing `step`, `await step("timestamp", () => Date.now())` records a
  stable value. Use JSON arguments/results.
- Offload blocking work to a worker so lease heartbeats run; await effects
  within the handler's lifetime.

Backend code uses `new Client()`, then
`await client.create("my-agent", { query: "hello" })`. Creation returns before
completion; inspect with `client.get` and `client.listSteps`.
`Client` reads `REBUNO_URL` and optional `REBUNO_API_KEY`.
