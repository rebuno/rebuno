// Demo agent: LangGraph agent with local tool execution (no runner needed).

import { z } from "zod";
import { ChatOpenAI } from "@langchain/openai";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { BaseAgent, AgentContext, defineTool } from "rebuno";
import { toLangchainTools } from "rebuno/tools/adapters/langchain";

const SYSTEM_PROMPT =
  "You are a research assistant. You have access to tools for searching the web, " +
  "fetching documents, and doing math. Use them to answer the user's question. " +
  "When you have enough information, provide a clear final answer.";

// LangGraph agent where tools run in the agent process.
//
// Same LLM reasoning as the remote-tools demo, but tools execute in-process
// instead of being dispatched to a separate runner. The kernel still
// enforces policy and records every tool call as events.
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

    // ctx.getTools() returns wrapped tools — adapters convert them to
    // framework-specific formats (here LangChain StructuredTool).
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

function safeEval(expr: string): number {
  const tokens = expr.match(/(\d+\.?\d*|[+\-*/()])/g);
  if (!tokens) throw new Error("Empty expression");
  let pos = 0;

  function parseExpr(): number {
    let result = parseTerm();
    while (pos < tokens!.length && (tokens![pos] === "+" || tokens![pos] === "-")) {
      const op = tokens![pos++];
      const right = parseTerm();
      result = op === "+" ? result + right : result - right;
    }
    return result;
  }

  function parseTerm(): number {
    let result = parseFactor();
    while (pos < tokens!.length && (tokens![pos] === "*" || tokens![pos] === "/")) {
      const op = tokens![pos++];
      const right = parseFactor();
      result = op === "*" ? result * right : result / right;
    }
    return result;
  }

  function parseFactor(): number {
    if (tokens![pos] === "(") {
      pos++;
      const result = parseExpr();
      pos++; // skip ')'
      return result;
    }
    if (tokens![pos] === "-") {
      pos++;
      return -parseFactor();
    }
    return parseFloat(tokens![pos++]);
  }

  return parseExpr();
}

const agent = new ResearchAgent({
  agentId: "researcher-local",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

// Register tools with defineTool(). The SDK uses the id, description, and
// inputSchema so that frameworks like LangGraph can generate schemas for the LLM.

agent.addTool(
  defineTool({
    id: "web.search",
    description: "Search the web for information about a topic.",
    inputSchema: z.object({ query: z.string() }),
    execute: async ({ query }) => {
      console.log(`Local web search: ${query}`);
      await new Promise((r) => setTimeout(r, 200));
      return {
        query,
        results: [
          { title: `Result for: ${query}`, snippet: `Info about ${query}.` },
          { title: `More on: ${query}`, snippet: `Details about ${query}.` },
        ],
      };
    },
  }),
);

agent.addTool(
  defineTool({
    id: "doc.fetch",
    description: "Fetch and read the contents of a document at a URL.",
    inputSchema: z.object({ url: z.string() }),
    execute: async ({ url }) => {
      console.log(`Local doc fetch: ${url}`);
      await new Promise((r) => setTimeout(r, 100));
      return {
        url,
        title: `Document from ${url}`,
        content: `Mock content fetched from ${url}.`,
        wordCount: 42,
      };
    },
  }),
);

agent.addTool(
  defineTool({
    id: "calculator",
    description: "Evaluate a mathematical expression. Example: '2 + 3 * 4'.",
    inputSchema: z.object({ expression: z.string() }),
    execute: async ({ expression }) => {
      console.log(`Local calculator: ${expression}`);
      const result = safeEval(expression);
      return { expression, result };
    },
  }),
);

agent.run();
