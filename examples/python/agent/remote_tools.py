"""Demo agent: LangGraph agent with remote tool execution (via runner)."""

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
logger = logging.getLogger("demo-agent-remote-tools")

SYSTEM_PROMPT = (
    "You are a research assistant. You have access to tools for searching the web, "
    "fetching documents, and doing math. Use them to answer the user's question. "
    "When you have enough information, provide a clear final answer."
)


class ResearchAgent(AsyncBaseAgent):
    """LangGraph agent where tools are executed by a remote runner.

    The agent declares tool schemas via @agent.remote_tool(). The kernel
    pushes tool calls to an idle runner via SSE. Results are delivered back
    to the agent via its own SSE connection.
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

        # ctx.get_tools() returns wrapped callables for both local and
        # remote tools. Remote tool wrappers send remote=True to the kernel,
        # which pushes the job to an idle runner via SSE.
        tools = ctx.get_tools()

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


agent = ResearchAgent(
    agent_id="researcher",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
    api_key="your-secret-token",
)

# Declare remote tool schemas. The function body is never called — only
# the signature and docstring are used for framework schema generation.
# The actual implementation lives in the runner (examples/runner/runner.py).

@agent.remote_tool("web.search")
async def web_search(query: str) -> dict:
    """Search the web for information about a topic."""
    ...

@agent.remote_tool("doc.fetch")
async def doc_fetch(url: str) -> dict:
    """Fetch and read the contents of a document at a URL."""
    ...

@agent.remote_tool("calculator")
async def calculator(expression: str) -> dict:
    """Evaluate a mathematical expression. Example: '2 + 3 * 4'."""
    ...

if __name__ == "__main__":
    asyncio.run(agent.run())
