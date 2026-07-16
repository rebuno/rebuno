import { env } from "node:process";

import { createOpenAI } from "@ai-sdk/openai";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { generateText, jsonSchema, stepCountIs, tool, type ToolSet } from "ai";
import { Agent, rebunoFetch, wrapMcpTools, type RebunoTool } from "rebuno";

const SYSTEM_PROMPT =
  "You are a helpful assistant with filesystem access. Use the filesystem tools " +
  "to list directories and read files, then answer the user's question.";

const MODEL = env.OPENAI_MODEL ?? "gpt-4o-mini";
const MCP_ROOT = env.MCP_ROOT ?? "/tmp";

async function process(input: { query: string }): Promise<Record<string, unknown>> {
  const transport = new StdioClientTransport({
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-filesystem", MCP_ROOT],
  });
  const client = new Client({ name: "rebuno-mcp-example", version: "0.1.0" });
  await client.connect(transport);
  try {
    const { tools: listed } = await client.listTools();
    const tools = wrapMcpTools(listed, {
      call: (name, args) => client.callTool({ name, arguments: args }),
      prefix: "fs",
    });
    console.log(`MCP tools: ${tools.map((t) => t.name).join(", ")}`);

    const openai = createOpenAI({ fetch: rebunoFetch });
    const { text } = await generateText({
      model: openai(MODEL),
      system: SYSTEM_PROMPT,
      prompt: input.query,
      tools: asAiTools(tools),
      stopWhen: stepCountIs(10),
    });
    console.log(`done: ${text}`);
    return { query: input.query, answer: text };
  } finally {
    await client.close();
  }
}

/** Expose durable Rebuno tools to the Vercel AI SDK; each execute routes through the kernel. */
function asAiTools(tools: RebunoTool[]): ToolSet {
  return Object.fromEntries(
    tools.map((t) => [
      t.name,
      tool({
        description: t.description,
        inputSchema: jsonSchema((t.inputSchema ?? { type: "object" }) as Record<string, unknown>),
        execute: (args) => t.execute(args as Record<string, unknown>),
      }),
    ]),
  );
}

const agent = new Agent("mcp", {
  secret: env.REBUNO_AGENT_SECRET ?? "mcp-secret",
  baseUrl: env.REBUNO_URL ?? "http://localhost:8080",
});
console.log("mcp agent listening on :5003");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5003) }, process);
