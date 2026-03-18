// Demo agent: LangGraph agent with MCP tools as local tools.
//
// MCP servers are registered at startup. Their tools become available as local
// tools that route through the kernel intent flow (policy-checked, event-logged).
// The LLM decides which tools to call via the standard ReAct loop.

import { z } from "zod";
import { ChatOpenAI } from "@langchain/openai";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { BaseAgent, AgentContext, defineTool } from "rebuno";
import { toLangchainTools } from "rebuno/tools/adapters/langchain";

const SYSTEM_PROMPT =
  "You are a helpful assistant with access to filesystem tools and documentation lookup. " +
  "Use the filesystem tools to read files and list directories. " +
  "Use the context7 tools to look up documentation for programming libraries. " +
  "When you have enough information, provide a clear final answer.";

// LangGraph agent with MCP-provided tools.
//
// MCP servers (filesystem, context7) are registered at startup. Their tools
// are discovered via the MCP protocol and made available as local tools.
// The kernel enforces policy and records every tool call as events.
class McpAgent extends BaseAgent {
  private model = process.env.OPENAI_MODEL ?? "gpt-4o-mini";

  async process(ctx: AgentContext) {
    const input = ctx.input as Record<string, unknown> | string;
    const query =
      typeof input === "object" && input !== null
        ? (input.query as string) ?? ""
        : String(input);

    if (!query) return { error: "No query provided" };

    console.log(`Processing query: ${query}`);

    // ctx.getTools() returns wrapped tools for all registered tools,
    // including MCP tools discovered from connected servers.
    const tools = await toLangchainTools(ctx.getTools()) as any;
    console.log(`Available tools: ${tools.map((t: any) => t.name).join(", ")}`);

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

const agent = new McpAgent({
  agentId: process.env.AGENT_ID ?? "mcp",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

// Add MCP servers — their tools become local tools
agent.mcpServer({
  name: "filesystem",
  command: "npx",
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
});

// Remote MCP tool served by a runner (see examples/runner/runner_mcp.ts)
agent.addRemoteTool(
  defineTool({
    id: "context7.query-docs",
    description:
      "Retrieves and queries up-to-date documentation and code examples from Context7 " +
      "for any programming library or framework.",
    inputSchema: z.object({
      query: z.string().describe("The question or task you need help with. Be specific and include relevant details."),
      libraryId: z.string().describe("Exact Context7-compatible library ID (e.g., '/mongodb/docs', '/vercel/next.js')."),
    }),
    execute: async () => ({}),
  }),
);

agent.run();
