import { describe, expect, it } from "vitest";
import { z } from "zod";
import { defineTool } from "../src/tools/index.js";
import { ToolRegistry } from "../src/tools/registry.js";

describe("defineTool", () => {
  it("creates a tool with correct shape", () => {
    const tool = defineTool({
      id: "web.search",
      description: "Search the web",
      inputSchema: z.object({ query: z.string() }),
      execute: async ({ query }) => ({ results: [query] }),
    });

    expect(tool.id).toBe("web.search");
    expect(tool.description).toBe("Search the web");
  });
});

describe("ToolRegistry", () => {
  it("adds and retrieves native tools", () => {
    const registry = new ToolRegistry();
    const tool = defineTool({
      id: "test.tool",
      description: "Test",
      inputSchema: z.object({ x: z.number() }),
      execute: async ({ x }) => x * 2,
    });

    registry.addTool(tool);
    expect(registry.hasTool("test.tool")).toBe(true);
    expect(registry.getToolIds()).toContain("test.tool");
  });

  it("removes tools", () => {
    const registry = new ToolRegistry();
    const tool = defineTool({
      id: "test.tool",
      description: "Test",
      inputSchema: z.object({}),
      execute: async () => null,
    });

    registry.addTool(tool);
    expect(registry.hasTool("test.tool")).toBe(true);

    registry.removeTool("test.tool");
    expect(registry.hasTool("test.tool")).toBe(false);
  });

  it("adds Vercel-style external tools", () => {
    const registry = new ToolRegistry();
    const vercelTool = {
      description: "Weather tool",
      parameters: z.object({ city: z.string() }),
      execute: async (input: { city: string }) => `Weather in ${input.city}`,
    };

    registry.addExternalTool("weather", vercelTool);
    expect(registry.hasTool("weather")).toBe(true);

    const wrapped = registry.getWrappedTools();
    expect(wrapped).toHaveLength(1);
    expect(wrapped[0].id).toBe("weather");
    expect(wrapped[0].description).toBe("Weather tool");
  });

  it("adds LangChain-style external tools", () => {
    const registry = new ToolRegistry();
    const langchainTool = {
      name: "search",
      description: "Search tool",
      schema: z.object({ query: z.string() }),
      invoke: async (input: { query: string }) => `Results for ${input.query}`,
    };

    registry.addExternalTool("search", langchainTool);
    expect(registry.hasTool("search")).toBe(true);
  });

  it("adds Mastra-style external tools", () => {
    const registry = new ToolRegistry();
    const mastraTool = {
      description: "Mastra tool",
      inputSchema: z.object({ data: z.string() }),
      execute: async (input: { data: string }) => input.data,
    };

    registry.addExternalTool("mastra", mastraTool);
    expect(registry.hasTool("mastra")).toBe(true);
  });

  it("rejects unknown tool shapes", () => {
    const registry = new ToolRegistry();
    expect(() => registry.addExternalTool("bad", { foo: "bar" })).toThrow(
      "Cannot detect framework",
    );
  });

  it("getWrappedTools includes both native and external", () => {
    const registry = new ToolRegistry();
    registry.addTool(
      defineTool({
        id: "native",
        description: "Native",
        inputSchema: z.object({}),
        execute: async () => null,
      }),
    );
    registry.addExternalTool("external", {
      description: "External",
      parameters: z.object({}),
      execute: async () => null,
    });

    const tools = registry.getWrappedTools();
    expect(tools).toHaveLength(2);
    expect(tools.map((t) => t.id)).toEqual(["native", "external"]);
  });
});
