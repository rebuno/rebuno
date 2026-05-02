"""Demo agent: LangGraph agent with MCP tools.

Two flavors of MCP are mixed in one tool list:
  - filesystem: a local stdio MCP server, started by the agent process
  - context7: tools hosted by a runner (see examples/runner/runner_mcp.py),
    discovered via the kernel directory

The kernel enforces policy and records every tool call as events.
"""

import logging
import os

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI

from rebuno import Agent, MCPServer, remote

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

MODEL = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")

agent = Agent(
    os.getenv("AGENT_ID", "mcp"),
    kernel_url=os.getenv("REBUNO_KERNEL_URL", "http://localhost:8080"),
)

# Local MCP: the agent process opens this transport itself.
filesystem = MCPServer(
    "filesystem",
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
)

# Remote tools: schemas come from the kernel directory. A runner advertises
# context7.* (see examples/runner/runner_mcp.py).
context7 = remote.Tools("context7")


async def process(query: str) -> dict:
    logger.info("Processing query: %s", query)

    tools = [*filesystem.tools, *context7.tools]
    logger.info("Available tools: %s", [t.__name__ for t in tools])

    llm = ChatOpenAI(model=MODEL, temperature=0)
    graph = create_agent(model=llm, tools=tools, system_prompt=SYSTEM_PROMPT)

    result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
    answer = result["messages"][-1].content
    logger.info("Agent finished: %s", answer)

    return {"query": query, "answer": answer}


if __name__ == "__main__":
    agent.run(process)
