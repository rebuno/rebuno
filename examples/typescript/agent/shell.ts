// Demo agent: LangGraph agent with shell execution (demonstrates approval flow).
//
// The shell.exec tool is governed by policy:
//   - Safe commands (ls, cat, pwd, ...) are auto-allowed by the agent policy
//   - Other commands require human approval via the client
//   - The global policy denies shell.exec for any agent without explicit rules

import { exec } from "node:child_process";
import { z } from "zod";
import { ChatOpenAI } from "@langchain/openai";
import { createReactAgent } from "@langchain/langgraph/prebuilt";
import { BaseAgent, AgentContext, defineTool } from "rebuno";
import { toLangchainTools } from "rebuno/tools/adapters/langchain";

const SYSTEM_PROMPT =
  "You are a helpful sysadmin assistant. You can run shell commands to answer " +
  "questions about the system. Use the shell tool to execute commands. Start with " +
  "safe read-only commands when possible.";

// LangGraph agent that executes shell commands under kernel policy.
class ShellAgent extends BaseAgent {
  private model = process.env.OPENAI_MODEL ?? "gpt-4o-mini";

  async process(ctx: AgentContext) {
    const input = ctx.input as Record<string, unknown> | string;
    const query =
      typeof input === "object" && input !== null
        ? (input.query as string) ?? ""
        : String(input);

    if (!query) return { error: "No query provided" };

    console.log(`Processing query: ${query}`);

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

const agent = new ShellAgent({
  agentId: "shell-assistant",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

agent.addTool(
  defineTool({
    id: "shell.exec",
    description: "Execute a shell command.",
    inputSchema: z.object({
      command: z.string(),
      timeout: z.number().default(30),
    }),
    execute: async ({ command, timeout = 30 }) => {
      console.log(`Executing: ${command}`);
      const timeoutMs = timeout * 1000;
      return new Promise<Record<string, unknown>>((resolve) => {
        const child = exec(
          command,
          { timeout: timeoutMs },
          (error: Error | null, stdout: string, stderr: string) => {
            if (
              error &&
              (error as NodeJS.ErrnoException & { killed?: boolean }).killed
            ) {
              resolve({
                exitCode: -1,
                stdout: "",
                stderr: "Command timed out",
              });
              return;
            }
            resolve({
              exitCode: (error as NodeJS.ErrnoException)?.code ?? 0,
              stdout: stdout ?? "",
              stderr: stderr ?? "",
            });
          },
        );
        setTimeout(() => child.kill(), timeoutMs);
      });
    },
  }),
);

agent.run();
