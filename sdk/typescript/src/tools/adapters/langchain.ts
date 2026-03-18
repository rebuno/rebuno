import type { WrappedTool } from "../index.js";

function sanitizeToolName(id: string): string {
  return id.replace(/[^a-zA-Z0-9_-]/g, "_");
}

export async function toLangchainTool(
  wrappedTool: WrappedTool,
): Promise<unknown> {
  const { tool } = await import("@langchain/core/tools" as string);
  return tool(
    async (input: unknown) => {
      const result = await wrappedTool.execute(input);
      return typeof result === "string" ? result : JSON.stringify(result);
    },
    {
      name: sanitizeToolName(wrappedTool.id),
      description: wrappedTool.description,
      schema: wrappedTool.inputSchema,
    },
  );
}

export async function toLangchainTools(
  tools: WrappedTool[],
): Promise<unknown[]> {
  return Promise.all(tools.map(toLangchainTool));
}
