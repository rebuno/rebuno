import { describe, expect, it } from "vitest";
import { z } from "zod";
import type { WrappedTool } from "../src/tools/index.js";
import { toVercelTool, toVercelTools } from "../src/tools/adapters/vercel.js";
import { toLangchainTool } from "../src/tools/adapters/langchain.js";
import { toMastraTool } from "../src/tools/adapters/mastra.js";

const sampleTool: WrappedTool = {
  id: "web.search",
  description: "Search the web",
  inputSchema: z.object({ query: z.string() }),
  execute: async (input) => {
    const { query } = input as { query: string };
    return { results: [query] };
  },
};

describe("Vercel adapter", () => {
  it("converts to Vercel CoreTool shape", () => {
    const vTool = toVercelTool(sampleTool);
    expect(vTool.description).toBe("Search the web");
    expect(vTool.parameters).toBe(sampleTool.inputSchema);
    expect(typeof vTool.execute).toBe("function");
  });

  it("execute works", async () => {
    const vTool = toVercelTool(sampleTool);
    const result = await vTool.execute({ query: "test" });
    expect(result).toEqual({ results: ["test"] });
  });

  it("converts multiple tools to record", () => {
    const tools = toVercelTools([sampleTool]);
    expect(Object.keys(tools)).toEqual(["web_search"]);
    expect(tools["web_search"].description).toBe("Search the web");
  });
});

describe("LangChain adapter", () => {
  it("toLangchainTool requires @langchain/core peer dep", async () => {
    // If @langchain/core is installed, the adapter produces a real StructuredTool.
    // If not, the dynamic import rejects. Either way is valid.
    try {
      const lcTool = (await toLangchainTool(sampleTool)) as Record<
        string,
        unknown
      >;
      // @langchain/core is installed — verify the shape
      expect(lcTool.name).toBe("web.search");
      expect(lcTool.description).toBe("Search the web");
      expect(typeof lcTool.invoke).toBe("function");

      const result = await (
        lcTool as { invoke: (i: unknown) => Promise<unknown> }
      ).invoke({ query: "test" });
      expect(result).toBe('{"results":["test"]}');
    } catch {
      // @langchain/core not installed — the async adapter correctly rejects
      await expect(toLangchainTool(sampleTool)).rejects.toThrow();
    }
  });
});

describe("Mastra adapter", () => {
  it("converts to Mastra shape", () => {
    const mTool = toMastraTool(sampleTool);
    expect(mTool.id).toBe("web.search");
    expect(mTool.description).toBe("Search the web");
    expect(mTool.inputSchema).toBe(sampleTool.inputSchema);
    expect(typeof mTool.execute).toBe("function");
  });

  it("execute passes context correctly", async () => {
    const mTool = toMastraTool(sampleTool);
    const result = await mTool.execute({ context: { query: "test" } });
    expect(result).toEqual({ results: ["test"] });
  });
});
