"""Demo agent: LangGraph agent with remote tool execution (via runner).

The agent declares tool stubs with @tool(remote=True). The kernel pushes
each invocation to an idle runner via SSE; results return on the agent's
own SSE connection. The actual tool implementations live in
``examples/runner/runner.py``.
"""

import logging
import os

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI

from rebuno import Agent, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("demo-agent-remote-tools")

SYSTEM_PROMPT = (
    "You are a research assistant. You have access to tools for searching the web, "
    "fetching documents, and doing math. Use them to answer the user's question. "
    "When you have enough information, provide a clear final answer."
)

MODEL = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")

agent = Agent(
    "researcher",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
    api_key="your-secret-token",
)


# Remote tool stubs: bodies are never called. Only signatures and docstrings
# are used for framework schema generation. The runner provides the
# implementation; the kernel routes each call to an idle runner.

@tool("web.search", remote=True)
async def web_search(query: str) -> dict:
    """Search the web for information about a topic."""


@tool("doc.fetch", remote=True)
async def doc_fetch(url: str) -> dict:
    """Fetch and read the contents of a document at a URL."""


@tool("calculator", remote=True)
async def calculator(expression: str) -> dict:
    """Evaluate a mathematical expression. Example: '2 + 3 * 4'."""


async def process(query: str) -> dict:
    logger.info("Processing query: %s", query)

    llm = ChatOpenAI(model=MODEL, temperature=0)
    graph = create_agent(
        model=llm,
        tools=[web_search, doc_fetch, calculator],
        system_prompt=SYSTEM_PROMPT,
    )

    result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
    answer = result["messages"][-1].content
    logger.info("Agent finished: %s", answer)

    return {"query": query, "answer": answer}


if __name__ == "__main__":
    agent.run(process)
