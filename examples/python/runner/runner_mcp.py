"""Example: MCP-backed runner that wraps filesystem and fetch MCP servers."""

import asyncio
import os

from rebuno import AsyncBaseRunner


async def main():
    runner = AsyncBaseRunner(
        runner_id=os.getenv("RUNNER_ID", "mcp-tools"),
        kernel_url=os.getenv("REBUNO_KERNEL_URL", "http://localhost:8080"),
    )

    # Stdio MCP server
    runner.mcp_server(
        "filesystem",
        command="npx",
        args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
    )

    # HTTP MCP server
    runner.mcp_server("context7", url="https://mcp.context7.com/mcp")

    await runner.run()


if __name__ == "__main__":
    asyncio.run(main())
