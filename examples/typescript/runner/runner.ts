// Demo runner: executes web.search, doc.fetch, and calculator tools
// on behalf of agents that use addRemoteTool().

import { BaseRunner } from "rebuno";

// Evaluate a simple arithmetic expression without eval().
function safeEval(expr: string): number {
  const tokens = expr.match(/(\d+\.?\d*|[+\-*/()])/g);
  if (!tokens) throw new Error("Empty expression");
  let pos = 0;

  function parseExpr(): number {
    let result = parseTerm();
    while (pos < tokens!.length && (tokens![pos] === "+" || tokens![pos] === "-")) {
      const op = tokens![pos++];
      const right = parseTerm();
      result = op === "+" ? result + right : result - right;
    }
    return result;
  }

  function parseTerm(): number {
    let result = parseFactor();
    while (pos < tokens!.length && (tokens![pos] === "*" || tokens![pos] === "/")) {
      const op = tokens![pos++];
      const right = parseFactor();
      result = op === "*" ? result * right : result / right;
    }
    return result;
  }

  function parseFactor(): number {
    if (tokens![pos] === "(") {
      pos++;
      const result = parseExpr();
      pos++; // skip ')'
      return result;
    }
    if (tokens![pos] === "-") {
      pos++;
      return -parseFactor();
    }
    return parseFloat(tokens![pos++]);
  }

  return parseExpr();
}

class DemoRunner extends BaseRunner {
  async execute(toolId: string, args: unknown): Promise<unknown> {
    const arguments_ = args as Record<string, unknown> | undefined;

    switch (toolId) {
      case "web.search":
        return this.webSearch(arguments_);
      case "doc.fetch":
        return this.docFetch(arguments_);
      case "calculator":
        return this.calculator(arguments_);
      default:
        throw new Error(`Unknown tool: ${toolId}`);
    }
  }

  private async webSearch(args: Record<string, unknown> | undefined) {
    const query = (args?.query as string) ?? "";
    console.log(`Mock web search: ${query}`);
    await new Promise((r) => setTimeout(r, 500));
    return {
      query,
      results: [
        {
          title: `Result 1 for: ${query}`,
          snippet: `This is a mock result about ${query}.`,
          url: `https://example.com/result1?q=${query}`,
        },
        {
          title: `Result 2 for: ${query}`,
          snippet: `Another mock result about ${query}.`,
          url: `https://example.com/result2?q=${query}`,
        },
      ],
      urls: [
        `https://example.com/result1?q=${query}`,
        `https://example.com/result2?q=${query}`,
      ],
    };
  }

  private async docFetch(args: Record<string, unknown> | undefined) {
    const url = (args?.url as string) ?? "";
    console.log(`Mock document fetch: ${url}`);
    await new Promise((r) => setTimeout(r, 300));
    return {
      url,
      title: `Document from ${url}`,
      content: `This is mock content fetched from ${url}. It contains information relevant to the query.`,
      wordCount: 42,
    };
  }

  private calculator(args: Record<string, unknown> | undefined) {
    const expression = (args?.expression as string) ?? "";
    console.log(`Mock calculator: ${expression}`);
    const result = safeEval(expression);
    return { expression, result };
  }
}

const runner = new DemoRunner({
  runnerId: "demo-runner",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
  capabilities: ["web.search", "doc.fetch", "calculator"],
  name: "Demo Runner",
});

runner.run();
