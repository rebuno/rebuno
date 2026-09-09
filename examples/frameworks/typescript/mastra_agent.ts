import { randomUUID } from "node:crypto";
import { env } from "node:process";

import { createOpenAI } from "@ai-sdk/openai";
import { Agent as MastraAgent } from "@mastra/core/agent";
import { createTool } from "@mastra/core/tools";
import { Agent, rebunoFetch, step } from "rebuno";

import { supportTools } from "./support.js";

async function process(input: { query: string }) {
  const modelName = env.LLM_MODEL;
  if (!modelName) throw new Error("LLM_MODEL is required");
  const ref = await step("brief_ref", () => randomUUID().replaceAll("-", "").slice(0, 8));
  const openai = createOpenAI({ fetch: rebunoFetch });

  const tools = Object.fromEntries(supportTools.map(({ name, description, schema, execute }) => [name, createTool({
    id: name, description, inputSchema: schema, execute,
  })]));

  const assistant = new MastraAgent({
    id: "support",
    name: "Support",
    instructions: "Investigate customer issues, create tickets, and email support summaries.",
    model: openai(modelName),
    maxRetries: 2,
    tools,
  });
  const { text } = await assistant.generate(`[${ref}] ${input.query}`, { maxSteps: 12 });
  return { answer: text, ref };
}

const agent = new Agent(env.AGENT_ID ?? "support-ts-mastra");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
