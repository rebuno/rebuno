// Demo agent: LangGraph agent with remote tool execution (via runner).

import { z } from "zod";
import { ChatOpenAI } from "@langchain/openai";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { BaseAgent, AgentContext, defineTool } from "rebuno";
import { toLangchainTools } from "rebuno/tools/adapters/langchain";

const SYSTEM_PROMPT =
  "You are a research assistant. You have access to tools for searching the web, " +
  "fetching documents, and doing math. Use them to answer the user's question. " +
  "When you have enough information, provide a clear final answer.";

// LangGraph agent where tools are executed by a remote runner.
//
// The agent declares tool schemas via addRemoteTool(). The kernel pushes
// tool calls to an idle runner via SSE. Results are delivered back to the
// agent via its own SSE connection.
class ResearchAgent extends BaseAgent {
  private model = process.env.OPENAI_MODEL ?? "gpt-4o-mini";

  async process(ctx: AgentContext) {
    const input = ctx.input as Record<string, unknown> | string;
    const query =
      typeof input === "object" && input !== null
        ? (input.query as string) ?? ""
        : String(input);

    if (!query) return { error: "No query provided" };

    console.log(`Processing query: ${query}`);

    // ctx.getTools() returns wrapped tools for both local and remote tools.
    // Remote tool wrappers send remote=true to the kernel, which pushes the
    // job to an idle runner via SSE.
    const tools = await toLangchainTools(ctx.getTools()) as any;

    const llm = new ChatOpenAI({ model: this.model, temperature: 0 });
    const agent = createReactAgent({
      llm,
      tools,
      stateModifier: SYSTEM_PROMPT,
    });

    const result = await agent.invoke({
      messages: [{ role: "user", content: query }],
    });

    const finalMessage = result.messages[result.messages.length - 1].content;
    const answer =
      typeof finalMessage === "string"
        ? finalMessage
        : JSON.stringify(finalMessage);
    console.log(`Agent finished: ${answer.slice(0, 200)}`);

    return { query, answer };
  }
}

const agent = new ResearchAgent({
  agentId: "researcher",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

// Declare remote tool schemas. The execute function is never called — only
// the id, description, and inputSchema are used for framework schema generation.
// The actual implementation lives in the runner (examples/runner/runner.ts).

agent.addRemoteTool(
  defineTool({
    id: "web.search",
    description: "Search the web for information about a topic.",
    inputSchema: z.object({ query: z.string() }),
    execute: async () => ({}),
  }),
);

agent.addRemoteTool(
  defineTool({
    id: "doc.fetch",
    description: "Fetch and read the contents of a document at a URL.",
    inputSchema: z.object({ url: z.string() }),
    execute: async () => ({}),
  }),
);

agent.addRemoteTool(
  defineTool({
    id: "calculator",
    description: "Evaluate a mathematical expression. Example: '2 + 3 * 4'.",
    inputSchema: z.object({ expression: z.string() }),
    execute: async () => ({}),
  }),
);

agent.run();
