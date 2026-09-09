import { randomUUID } from "node:crypto";
import { env } from "node:process";

import { tool } from "@langchain/core/tools";
import { ChatOpenAI } from "@langchain/openai";
import { createAgent } from "langchain";
import { Agent, rebunoFetch, step } from "rebuno";

import { supportTools } from "./support.js";

async function process(input: { query: string }) {
  const modelName = env.LLM_MODEL;
  if (!modelName) throw new Error("LLM_MODEL is required");
  const ref = await step("brief_ref", () => randomUUID().replaceAll("-", "").slice(0, 8));
  const model = new ChatOpenAI({ model: modelName, configuration: { fetch: rebunoFetch } });

  // The agent loop keeps calling the model after a tool is held for approval,
  // and each call is refused. Aborting on the first one ends the loop instead.
  const halt = new AbortController();
  const tools = supportTools.map(({ name, description, schema, execute }) => tool(
    async (args: Record<string, unknown>) => {
      try { return await execute(args); }
      catch (error) { halt.abort(error); throw error; }
    },
    { name, description, schema },
  ));

  const graph = createAgent({ model, tools });
  const result = await graph.invoke(
    { messages: [{ role: "user", content: `[${ref}] ${input.query}` }] },
    { signal: halt.signal },
  );
  return { answer: result.messages.at(-1)?.content, ref };
}

const agent = new Agent(env.AGENT_ID ?? "support-ts-langchain");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
