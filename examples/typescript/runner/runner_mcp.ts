// Example: MCP-backed runner that wraps filesystem and fetch MCP servers.

import { BaseRunner } from "rebuno";

const runner = new (class extends BaseRunner {})({
  runnerId: process.env.RUNNER_ID ?? "mcp-tools",
  kernelUrl: process.env.REBUNO_KERNEL_URL ?? "http://localhost:8080",
});

// Stdio MCP server
runner.mcpServer({
  name: "filesystem",
  command: "npx",
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
});

// HTTP MCP server
runner.mcpServer({
  name: "context7",
  url: "https://mcp.context7.com/mcp",
});

runner.run();
