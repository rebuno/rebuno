import ast
import asyncio
import logging
import operator
import os

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI

from rebuno import Agent, http_client, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("researcher")

SYSTEM_PROMPT = (
    "You are a research assistant. You have access to tools for searching the web, "
    "fetching documents, and doing math. Use them to answer the user's question. "
    "When you have enough information, provide a clear final answer."
)

MODEL = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")


@tool("web_search")
async def web_search(query: str) -> dict:
    """Search the web for information about a topic."""
    logger.info("web_search: %s", query)
    await asyncio.sleep(0.2)
    return {
        "query": query,
        "results": [
            {"title": f"Result for: {query}", "snippet": f"Info about {query}."},
            {"title": f"More on: {query}", "snippet": f"Details about {query}."},
        ],
    }


@tool("doc_fetch")
async def doc_fetch(url: str) -> dict:
    """Fetch and read the contents of a document at a URL."""
    logger.info("doc_fetch: %s", url)
    await asyncio.sleep(0.1)
    return {
        "url": url,
        "title": f"Document from {url}",
        "content": f"Mock content fetched from {url}.",
        "word_count": 42,
    }


@tool("calculator")
async def calculator(expression: str) -> dict:
    """Evaluate a mathematical expression. Example: '2 + 3 * 4'."""
    logger.info("calculator: %s", expression)
    return {"expression": expression, "result": _safe_eval(expression)}


async def process(query: str) -> dict:
    llm = ChatOpenAI(model=MODEL, temperature=0, http_async_client=http_client())
    graph = create_agent(
        model=llm,
        tools=[web_search, doc_fetch, calculator],
        system_prompt=SYSTEM_PROMPT,
    )
    result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
    answer = result["messages"][-1].content
    logger.info("done: %s", answer)
    return {"query": query, "answer": answer}


_OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.USub: operator.neg,
    ast.UAdd: operator.pos,
}


def _safe_eval(expr: str) -> float:
    return _eval_node(ast.parse(expr, mode="eval").body)


def _eval_node(node: ast.AST) -> float:
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return node.value
    if isinstance(node, ast.BinOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.left), _eval_node(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.operand))
    raise ValueError(f"Unsupported expression: {type(node).__name__}")


if __name__ == "__main__":
    agent = Agent(
        "researcher",
        secret=os.environ.get("REBUNO_AGENT_SECRET", "researcher-secret"),
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    logger.info("researcher agent listening on :5001")
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5001")))
