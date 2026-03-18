import { z, type ZodType } from "zod";
import type { RebunoTool, WrappedTool } from "./index.js";

export type ToolFormat = "vercel" | "langchain" | "mastra";

export class ToolRegistry {
  private tools = new Map<string, RebunoTool>();
  private externalTools = new Map<string, ExternalToolEntry>();

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  addTool(tool: RebunoTool<any, any>): void {
    this.tools.set(tool.id, tool);
  }

  removeTool(id: string): void {
    this.tools.delete(id);
    this.externalTools.delete(id);
  }

  addExternalTool(id: string, tool: unknown): void {
    const entry = normalizeExternal(id, tool);
    this.externalTools.set(id, entry);
  }

  getToolIds(): string[] {
    return [...this.tools.keys(), ...this.externalTools.keys()];
  }

  hasTool(id: string): boolean {
    return this.tools.has(id) || this.externalTools.has(id);
  }

  getExecute(id: string): ((input: unknown) => Promise<unknown>) | undefined {
    const native = this.tools.get(id);
    if (native) return native.execute as (input: unknown) => Promise<unknown>;
    const ext = this.externalTools.get(id);
    if (ext) return ext.execute;
    return undefined;
  }

  getWrappedTools(): WrappedTool[] {
    const result: WrappedTool[] = [];

    for (const tool of this.tools.values()) {
      result.push({
        id: tool.id,
        description: tool.description,
        inputSchema: tool.inputSchema,
        execute: tool.execute as (input: unknown) => Promise<unknown>,
      });
    }

    for (const [id, entry] of this.externalTools) {
      result.push({
        id,
        description: entry.description,
        inputSchema: entry.inputSchema,
        execute: entry.execute,
      });
    }

    return result;
  }
}

interface ExternalToolEntry {
  description: string;
  inputSchema: ZodType;
  execute: (input: unknown) => Promise<unknown>;
}

function normalizeExternal(id: string, tool: unknown): ExternalToolEntry {
  if (!tool || typeof tool !== "object") {
    throw new Error(`External tool '${id}' must be an object`);
  }

  const t = tool as Record<string, unknown>;

  if ("parameters" in t && isZodType(t.parameters)) {
    return {
      description: (t.description as string) ?? "",
      inputSchema: t.parameters as ZodType,
      execute: typeof t.execute === "function"
        ? (t.execute as (input: unknown) => Promise<unknown>)
        : async () => {
            throw new Error(`Vercel tool '${id}' has no execute function`);
          },
    };
  }

  if ("schema" in t && isZodType(t.schema) && typeof t.invoke === "function") {
    return {
      description: (t.description as string) ?? "",
      inputSchema: t.schema as ZodType,
      execute: t.invoke as (input: unknown) => Promise<unknown>,
    };
  }

  if ("inputSchema" in t && isZodType(t.inputSchema) && typeof t.execute === "function") {
    return {
      description: (t.description as string) ?? "",
      inputSchema: t.inputSchema as ZodType,
      execute: t.execute as (input: unknown) => Promise<unknown>,
    };
  }

  if (typeof t.execute === "function") {
    return {
      description: (t.description as string) ?? "",
      inputSchema: (isZodType(t.inputSchema) ? t.inputSchema : z.object({})) as ZodType,
      execute: t.execute as (input: unknown) => Promise<unknown>,
    };
  }

  throw new Error(
    `Cannot detect framework for external tool '${id}'. ` +
      `Expected a Vercel CoreTool, LangChain StructuredTool, Mastra tool, or object with execute().`,
  );
}

function isZodType(value: unknown): value is ZodType {
  return (
    value != null &&
    typeof value === "object" &&
    "_def" in (value as object) &&
    "parse" in (value as object)
  );
}
