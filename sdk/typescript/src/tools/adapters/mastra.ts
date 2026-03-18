import type { WrappedTool } from "../index.js";

export function toMastraTool(tool: WrappedTool): {
  id: string;
  description: string;
  inputSchema: WrappedTool["inputSchema"];
  execute: (params: { context: unknown }) => Promise<unknown>;
} {
  return {
    id: tool.id,
    description: tool.description,
    inputSchema: tool.inputSchema,
    execute: ({ context }: { context: unknown }) => tool.execute(context),
  };
}

export function toMastraTools(
  tools: WrappedTool[],
): Array<{
  id: string;
  description: string;
  inputSchema: WrappedTool["inputSchema"];
  execute: (params: { context: unknown }) => Promise<unknown>;
}> {
  return tools.map(toMastraTool);
}
