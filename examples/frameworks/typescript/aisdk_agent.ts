import { randomUUID } from "node:crypto";
import { env } from "node:process";

import { createOpenAI } from "@ai-sdk/openai";
import { generateText, streamText, stepCountIs, tool } from "ai";
import { Agent, rebunoFetch, step } from "rebuno";

import { supportTools } from "./support.js";

async function process(input: { query: string }) {
  const modelName = env.LLM_MODEL;
  if (!modelName) throw new Error("LLM_MODEL is required");
  const ref = await step("brief_ref", () => randomUUID().replaceAll("-", "").slice(0, 8));
  const openai = createOpenAI({ fetch: rebunoFetch });
  const streaming = env.LLM_STREAM === "1";
  const halt = new AbortController();
  const options = {
    model: streaming ? openai.chat(modelName) : openai(modelName),
    prompt: `[${ref}] ${input.query}`,
    tools: Object.fromEntries(supportTools.map(({ name, description, schema, execute }) => [name, tool({
      description, inputSchema: schema,
      execute: async (args: Record<string, unknown>) => {
        try { return await execute(args); }
        catch (error) { halt.abort(error); throw error; }
      },
    })])),
    stopWhen: stepCountIs(12),
    abortSignal: halt.signal,
  };
  let text: string;
  try {
    if (streaming) {
      const result = streamText(options);
      // Stream errors are events; awaiting text alone can hide an incomplete response.
      for await (const part of result.fullStream) {
        if (part.type === "error" || part.type === "tool-error") throw part.error;
      }
      if (await result.finishReason !== "stop") {
        throw new Error("Model stream ended without a complete answer");
      }
      text = await result.text;
    } else {
      text = (await generateText(options)).text;
    }
  } catch (error) {
    throw halt.signal.reason ?? error;
  }
  return { answer: text, ref };
}

const agent = new Agent(env.AGENT_ID ?? "support-ts-aisdk");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
