import { randomUUID } from "node:crypto";
import { env } from "node:process";

import { Agent as OpenAIAgent, OpenAIResponsesModel, run, tool } from "@openai/agents";
import OpenAI from "openai";
import { Agent, rebunoFetch, step } from "rebuno";

import { supportTools } from "./support.js";

async function process(input: { query: string }) {
  const modelName = env.LLM_MODEL;
  if (!modelName) throw new Error("LLM_MODEL is required");
  const ref = await step("brief_ref", () => randomUUID().replaceAll("-", "").slice(0, 8));
  const client = new OpenAI({ fetch: rebunoFetch });

  const writer = new OpenAIAgent({
    name: "support specialist",
    model: new OpenAIResponsesModel(client, modelName),
    // Without an error function a Blocked tool unwinds the run instead of
    // being handed to the model as an error message.
    tools: supportTools.map(({ name, description, schema, execute }) => tool({
      name, description, parameters: schema, execute, errorFunction: null,
    })),
  });
  const result = await run(writer, `[${ref}] ${input.query}`);
  return { answer: result.finalOutput, ref };
}

const agent = new Agent(env.AGENT_ID ?? "support-ts-openai-agents");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
