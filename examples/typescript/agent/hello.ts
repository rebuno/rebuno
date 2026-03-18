import { z } from "zod";
import { BaseAgent, AgentContext, defineTool } from "rebuno";

const agent = new (class HelloAgent extends BaseAgent {
  async process(ctx: AgentContext) {
    const input = ctx.input as Record<string, unknown> | string;
    const query =
      typeof input === "object" && input !== null
        ? (input.query as string) ?? ""
        : String(input);

    console.log(`Processing query: ${query}`);

    const reverseResult = (await ctx.invokeTool("reverse", {
      text: query,
    })) as Record<string, unknown>;
    const countResult = (await ctx.invokeTool("word_count", {
      text: query,
    })) as Record<string, unknown>;

    console.log(
      `Agent finished: reversed=${reverseResult.reversed}, count=${countResult.count}`,
    );

    return {
      query,
      reversed: reverseResult.reversed,
      wordCount: countResult.count,
    };
  }
})({
  agentId: "hello",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

agent.addTool(
  defineTool({
    id: "reverse",
    description: "Reverse a string.",
    inputSchema: z.object({ text: z.string() }),
    execute: async ({ text }) => ({ reversed: text.split("").reverse().join("") }),
  }),
);

agent.addTool(
  defineTool({
    id: "word_count",
    description: "Count words in a string.",
    inputSchema: z.object({ text: z.string() }),
    execute: async ({ text }) => ({ count: text.split(/\s+/).filter(Boolean).length }),
  }),
);

console.log("Starting hello agent");
agent.run();
