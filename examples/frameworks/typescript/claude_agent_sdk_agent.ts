import { randomUUID } from "node:crypto";
import { env } from "node:process";

import { createSdkMcpServer, query, tool } from "@anthropic-ai/claude-agent-sdk";
import { Agent, execution, step } from "rebuno";

import { supportTools } from "./support.js";

async function process(input: { query: string }) {
  const modelName = env.LLM_MODEL;
  if (!modelName) throw new Error("LLM_MODEL is required");
  const { GATEWAY_URL, GATEWAY_KEY, REBUNO_AGENT_SECRET } = env;
  if (!GATEWAY_URL || !GATEWAY_KEY || !REBUNO_AGENT_SECRET) {
    throw new Error("GATEWAY_URL, GATEWAY_KEY, and REBUNO_AGENT_SECRET are required");
  }
  const ref = await step("brief_ref", () => randomUUID().replaceAll("-", "").slice(0, 8));
  const ctx = execution();
  const lease = {
    "rebuno-execution-id": ctx.id,
    "rebuno-dispatch-id": ctx.dispatchId,
    "rebuno-dispatch-attempt": String(ctx.dispatchAttempt),
    "rebuno-agent-id": ctx.agentId,
    "rebuno-agent-secret": REBUNO_AGENT_SECRET,
  };

  // The CLI hands a tool's error to the model and keeps retrying the refused
  // model call after a tool is held for approval. Aborting on the first tool
  // error ends the run instead.
  const halt = new AbortController();
  const support = createSdkMcpServer({
    name: "support",
    tools: supportTools.map(({ name, description, schema, execute }) => tool(
      name, description, schema.shape,
      async (args) => {
        try {
          const result = await execute(args);
          const text = typeof result === "string" ? result : JSON.stringify(result);
          return { content: [{ type: "text", text }] };
        } catch (error) {
          halt.abort(error);
          throw error;
        }
      },
    )),
  });

  let answer: string | undefined;
  try {
    for await (const message of query({
      prompt: `[${ref}] ${input.query}`,
      options: {
        abortController: halt,
        model: modelName,
        systemPrompt: "You investigate customer issues, create tickets, and email support summaries.",
        // Built-in tools run inside the CLI, out of the kernel's sight, so only
        // the Rebuno-routed MCP tools are exposed.
        tools: [],
        mcpServers: { support },
        allowedTools: supportTools.map(({ name }) => `mcp__support__${name}`),
        settingSources: [],
        // The CLI makes the model calls from its own process, so they reach the
        // kernel through a Rebuno-compatible gateway rather than rebunoFetch.
        env: {
          ...env,
          ANTHROPIC_BASE_URL: GATEWAY_URL,
          ANTHROPIC_AUTH_TOKEN: GATEWAY_KEY,
          ANTHROPIC_CUSTOM_HEADERS: Object.entries(lease).map(([k, v]) => `${k}: ${v}`).join("\n"),
          CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
        },
      },
    })) {
      if (message.type !== "result") continue;
      if (message.subtype !== "success" || message.is_error) {
        throw new Error("result" in message ? message.result : message.subtype);
      }
      answer = message.result;
    }
  } catch (error) {
    throw halt.signal.reason ?? error;
  }
  return { answer, ref };
}

const agent = new Agent(env.AGENT_ID ?? "support-ts-claude-agent-sdk");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
