"""Demo agent: LangGraph agent with local tool execution (no runner needed)."""

import ast
import asyncio
import logging
import operator
import os
from typing import Any

from langchain_openai import ChatOpenAI
from langchain.agents import create_agent

from rebuno import AsyncAgentContext, AsyncBaseAgent

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("demo-agent-local-tools")

SYSTEM_PROMPT = (
    "You are a research assistant. You have access to tools for searching the web, "
    "fetching documents, and doing math. Use them to answer the user's question. "
    "When you have enough information, provide a clear final answer."
)


class ResearchAgent(AsyncBaseAgent):
    """LangGraph agent where tools run in the agent process.

    Same LLM reasoning as the remote-tools demo, but tools execute in-process
    instead of being dispatched to a separate runner. The kernel still
    enforces policy and records every tool call as events.
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

        # ctx.get_tools() returns wrapped callables with the original
        # signatures and docstrings — any framework can consume them directly.
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


_OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.USub: operator.neg,
    ast.UAdd: operator.pos,
}


def _safe_eval(expr: str) -> float:
    tree = ast.parse(expr, mode="eval")
    return _eval_node(tree.body)


def _eval_node(node: ast.AST) -> float:
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return node.value
    if isinstance(node, ast.BinOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.left), _eval_node(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.operand))
    raise ValueError(f"Unsupported expression: {type(node).__name__}")


agent = ResearchAgent(
    agent_id="researcher-local",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
    api_key="your-secret-token",
)

# Register tools with typed signatures. The SDK preserves the function
# name, docstring, and type annotations so that frameworks like LangGraph
# can introspect them to generate schemas for the LLM.

@agent.tool("web.search")
async def web_search(query: str) -> dict:
    """Search the web for information about a topic."""
    logger.info("Local web search: %s", query)
    await asyncio.sleep(0.2)
    return {
        "query": query,
        "results": [
            {"title": f"Result for: {query}", "snippet": f"Info about {query}."},
            {"title": f"More on: {query}", "snippet": f"Details about {query}."},
        ],
    }

@agent.tool("doc.fetch")
async def doc_fetch(url: str) -> dict:
    """Fetch and read the contents of a document at a URL."""
    logger.info("Local doc fetch: %s", url)
    await asyncio.sleep(0.1)
    return {
        "url": url,
        "title": f"Document from {url}",
        "content": f"Mock content fetched from {url}.",
        "word_count": 42,
    }

@agent.tool("calculator")
def calculator(expression: str) -> dict:
    """Evaluate a mathematical expression. Example: '2 + 3 * 4'."""
    logger.info("Local calculator: %s", expression)
    result = _safe_eval(expression)
    return {"expression": expression, "result": result}


if __name__ == "__main__":
    asyncio.run(agent.run())
