import { env } from "node:process";

import { Agent, defineTool } from "rebuno";

const reverse = defineTool({
  name: "reverse",
  description: "Reverse a string.",
  execute: async ({ text }: { text: string }) => ({ reversed: [...text].reverse().join("") }),
});

const wordCount = defineTool({
  name: "word_count",
  description: "Count words in a string.",
  execute: async ({ text }: { text: string }) => ({ count: text.split(/\s+/).filter(Boolean).length }),
});

async function process(input: { query: string }): Promise<Record<string, unknown>> {
  const { reversed } = await reverse.execute({ text: input.query });
  const { count } = await wordCount.execute({ text: input.query });
  console.log(`reversed=${reversed} count=${count}`);
  return { query: input.query, reversed, word_count: count };
}

const agent = new Agent("hello", {
  secret: env.REBUNO_AGENT_SECRET ?? "hello-secret",
  kernelUrl: env.REBUNO_URL ?? "http://localhost:8080",
});
console.log("hello agent listening on :5000");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5000) }, process);
