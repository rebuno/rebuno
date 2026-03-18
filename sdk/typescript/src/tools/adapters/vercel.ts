import type { WrappedTool } from "../index.js";

function sanitizeToolName(id: string): string {
  return id.replace(/[^a-zA-Z0-9_-]/g, "_");
}

export function toVercelTool(tool: WrappedTool): {
  description: string;
  parameters: WrappedTool["inputSchema"];
  execute: (input: unknown) => Promise<unknown>;
} {
  return {
    description: tool.description,
    parameters: tool.inputSchema,
    execute: (input: unknown) => tool.execute(input),
  };
}

export function toVercelTools(
  tools: WrappedTool[],
): Record<
  string,
  {
    description: string;
    parameters: WrappedTool["inputSchema"];
    execute: (input: unknown) => Promise<unknown>;
  }
> {
  const result: Record<string, ReturnType<typeof toVercelTool>> = {};
  for (const tool of tools) {
    result[sanitizeToolName(tool.id)] = toVercelTool(tool);
  }
  return result;
}
