"""Example: MCP-backed runner that hosts filesystem and context7 MCP servers."""

import os

from rebuno import MCPServer, Runner

if __name__ == "__main__":
    runner = Runner(
        os.getenv("RUNNER_ID", "mcp-tools"),
        kernel_url=os.getenv("REBUNO_KERNEL_URL", "http://localhost:8080"),
    )

    # Stdio MCP server
    runner.host(MCPServer(
        "filesystem",
        command="npx",
        args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
    ))

    # HTTP MCP server
    runner.host(MCPServer("context7", url="https://mcp.context7.com/mcp"))

    runner.run()
