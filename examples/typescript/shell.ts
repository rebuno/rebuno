/**
 * Demo agent: an LLM agent with shell execution (demonstrates the approval flow).
 *
 * The shell_exec tool is governed by policy:
 *   - Safe commands (ls, cat, pwd, ...) are auto-allowed by the agent policy
 *   - Other commands require human approval via the client
 *   - The global policy denies shell_exec for any agent without explicit rules
 */
import { exec } from "node:child_process";
import { env } from "node:process";

import { createOpenAI } from "@ai-sdk/openai";
import { generateText, jsonSchema, stepCountIs, tool } from "ai";
import { Agent, defineTool, rebunoFetch, type RebunoTool } from "rebuno";

const SYSTEM_PROMPT =
  "You are a helpful sysadmin assistant. You can run shell commands to answer " +
  "questions about the system. Use the shell tool to execute commands. Start with " +
  "safe read-only commands when possible.";

const MODEL = env.OPENAI_MODEL ?? "gpt-4o-mini";

const shellExec = defineTool({
  name: "shell_exec",
  description: "Execute a shell command.",
  idempotency: "at_most_once",
  inputSchema: {
    type: "object",
    properties: { command: { type: "string" }, timeout: { type: "number", default: 30 } },
    required: ["command"],
  },
  execute: async ({ command, timeout = 30 }: { command: string; timeout?: number }) => {
    console.log(`executing: ${command}`);
    return await new Promise<Record<string, unknown>>((resolve) => {
      exec(command, { timeout: timeout * 1000, encoding: "utf8" }, (err, stdout, stderr) => {
        if (err && (err as NodeJS.ErrnoException & { killed?: boolean }).killed) {
          resolve({ exit_code: -1, stdout: "", stderr: "Command timed out" });
          return;
        }
        resolve({ exit_code: err?.code ?? 0, stdout, stderr });
      });
    });
  },
});

async function process(input: { query: string }): Promise<Record<string, unknown>> {
  const openai = createOpenAI({ fetch: rebunoFetch });
  const { text } = await generateText({
    model: openai(MODEL),
    system: SYSTEM_PROMPT,
    prompt: input.query,
    tools: { shell_exec: asAiTool(shellExec) },
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

const agent = new Agent("shell", {
  secret: env.REBUNO_AGENT_SECRET ?? "shell-secret",
  kernelUrl: env.REBUNO_URL ?? "http://localhost:8080",
});
console.log("shell agent listening on :5002");
await agent.serve({ port: Number(env.AGENT_PORT ?? 5002) }, process);
