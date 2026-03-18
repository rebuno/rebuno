import type { ZodType } from "zod";

export interface RebunoTool<TInput = unknown, TOutput = unknown> {
  id: string;
  description: string;
  inputSchema: ZodType<TInput>;
  execute: (input: TInput) => Promise<TOutput>;
}

export interface WrappedTool {
  id: string;
  description: string;
  inputSchema: ZodType;
  execute: (input: unknown) => Promise<unknown>;
}

export function defineTool<TInput, TOutput>(
  tool: RebunoTool<TInput, TOutput>,
): RebunoTool<TInput, TOutput> {
  return tool;
}
