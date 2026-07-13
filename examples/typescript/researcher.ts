import { env } from "node:process";

import { createOpenAI } from "@ai-sdk/openai";
import { generateText, jsonSchema, stepCountIs, tool } from "ai";
import { Agent, defineTool, rebunoFetch, type RebunoTool } from "rebuno";

const SYSTEM_PROMPT =
  "You are a research assistant. You have access to tools for searching the web, " +
  "fetching documents, and doing math. Use them to answer the user's question. " +
  "When you have enough information, provide a clear final answer.";

const MODEL = env.OPENAI_MODEL ?? "gpt-4o-mini";

const webSearch = defineTool({
  name: "web_search",
  description: "Search the web for information about a topic.",
  inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] },
  execute: async ({ query }: { query: string }) => {
    console.log(`web_search: ${query}`);
    await sleep(200);
    return {
      query,
      results: [
        { title: `Result for: ${query}`, snippet: `Info about ${query}.` },
        { title: `More on: ${query}`, snippet: `Details about ${query}.` },
      ],
    };
  },
});

const docFetch = defineTool({
  name: "doc_fetch",
  description: "Fetch and read the contents of a document at a URL.",
  inputSchema: { type: "object", properties: { url: { type: "string" } }, required: ["url"] },
  execute: async ({ url }: { url: string }) => {
    console.log(`doc_fetch: ${url}`);
    await sleep(100);
    return { url, title: `Document from ${url}`, content: `Mock content fetched from ${url}.`, word_count: 42 };
  },
});

const calculator = defineTool({
  name: "calculator",
  description: "Evaluate a mathematical expression. Example: '2 + 3 * 4'.",
  inputSchema: { type: "object", properties: { expression: { type: "string" } }, required: ["expression"] },
  execute: async ({ expression }: { expression: string }) => {
    console.log(`calculator: ${expression}`);
    return { expression, result: safeEval(expression) };
  },
});

async function process(input: { query: string }): Promise<Record<string, unknown>> {
  const openai = createOpenAI({ fetch: rebunoFetch });
  const { text } = await generateText({
    model: openai(MODEL),
    system: SYSTEM_PROMPT,
    prompt: input.query,
    tools: {
      web_search: asAiTool(webSearch),
      doc_fetch: asAiTool(docFetch),
      calculator: asAiTool(calculator),
    },
    stopWhen: stepCountIs(10),
  });
  console.log(`done: ${text}`);
  return { query: input.query, answer: text };
}

/** Expose a durable Rebuno tool to the Vercel AI SDK; its execute routes through the kernel. */
function asAiTool(t: RebunoTool<any, any>) {
  return tool({
    description: t.description,
    inputSchema: jsonSchema(t.inputSchema as Record<string, unknown>),
    execute: (args) => t.execute(args as Record<string, unknown>),
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

function safeEval(expr: string): number {
  if (!/^[\d+\-*/().\s]+$/.test(expr)) throw new Error(`Unsupported expression: ${expr}`);
  return Function(`"use strict"; return (${expr});`)() as number;
}

const agent = new Agent("researcher", {
  secret: env.REBUNO_AGENT_SECRET ?? "researcher-secret",
  kernelUrl: env.REBUNO_URL ?? "http://localhost:8080",
});
console.log("researcher agent listening on :5001");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5001) }, process);
