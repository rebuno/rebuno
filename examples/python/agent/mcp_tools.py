"""Demo agent: LangGraph agent with MCP tools as local tools.

MCP servers are registered at startup. Their tools become available as local
tools that route through the kernel intent flow (policy-checked, event-logged).
The LLM decides which tools to call via the standard ReAct loop.
"""

import asyncio
import logging
import os
from typing import Any

from langchain_openai import ChatOpenAI
from langchain.agents import create_agent

from rebuno import AsyncAgentContext, AsyncBaseAgent

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("demo-agent-mcp")

SYSTEM_PROMPT = (
    "You are a helpful assistant with access to filesystem tools and documentation lookup. "
    "Use the filesystem tools to read files and list directories. "
    "Use the context7 tools to look up documentation for programming libraries. "
    "When you have enough information, provide a clear final answer."
)


class McpAgent(AsyncBaseAgent):
    """LangGraph agent with MCP-provided tools.

    MCP servers (filesystem, context7) are registered at startup. Their tools
    are discovered via the MCP protocol and made available as local tools.
    The kernel enforces policy and records every tool call as events.
    """

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self._model = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")

    async def process(self, ctx: AsyncAgentContext) -> dict:
        query = ""
        if isinstance(ctx.input, dict):
            query = ctx.input.get("query", "")
        elif isinstance(ctx.input, str):
            query = ctx.input

        if not query:
            return {"error": "No query provided"}

        logger.info("Processing query: %s", query)

        # ctx.get_tools() returns wrapped callables for all registered tools,
        # including MCP tools discovered from connected servers.
        tools = ctx.get_tools()
        logger.info("Available tools: %s", [t.__name__ for t in tools])

        llm = ChatOpenAI(model=self._model, temperature=0)
        agent = create_agent(
            model=llm,
            tools=tools,
            system_prompt=SYSTEM_PROMPT,
        )

        result = await agent.ainvoke(
            {"messages": [{"role": "user", "content": query}]}
        )

        final_message = result["messages"][-1].content
        logger.info("Agent finished: %s", final_message)

        return {"query": query, "answer": final_message}


agent = McpAgent(
    agent_id=os.getenv("AGENT_ID", "mcp"),
    kernel_url=os.getenv("REBUNO_KERNEL_URL", "http://localhost:8080"),
)

# Add MCP servers — their tools become local tools
agent.mcp_server(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)

# Remote MCP tool served by a runner (see examples/runner/runner_mcp.py)
@agent.remote_tool("context7.query-docs")
async def context7_query_docs(query: str, libraryId: str) -> dict:
    """Retrieves and queries up-to-date documentation and code examples from Context7 for any programming library or framework.

    Args:
        query: The question or task you need help with. Be specific and include relevant details.
        libraryId: Exact Context7-compatible library ID (e.g., '/mongodb/docs', '/vercel/next.js').
    Returns:
        A dictionary containing the results of the search.
    """
    ...


if __name__ == "__main__":
    asyncio.run(agent.run())
