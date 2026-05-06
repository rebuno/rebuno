"""Demo runner: services web_search, doc_fetch, and calculator tools.

Pair with examples/agent/remote_tools.py — the agent declares stubs for
these tool IDs, the runner provides implementations. The kernel routes
each call.
"""

import ast
import asyncio
import logging
import operator
import os

from rebuno import Runner, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("demo-runner")


@tool("web_search")
async def web_search(query: str) -> dict:
    """Search the web for information about a topic."""
    logger.info("Mock web search: %s", query)
    await asyncio.sleep(0.5)
    return {
        "query": query,
        "results": [
            {
                "title": f"Result 1 for: {query}",
                "snippet": f"This is a mock result about {query}.",
                "url": f"https://example.com/result1?q={query}",
            },
            {
                "title": f"Result 2 for: {query}",
                "snippet": f"Another mock result about {query}.",
                "url": f"https://example.com/result2?q={query}",
            },
        ],
        "urls": [
            f"https://example.com/result1?q={query}",
            f"https://example.com/result2?q={query}",
        ],
    }


@tool("doc_fetch")
async def doc_fetch(url: str) -> dict:
    """Fetch and read the contents of a document at a URL."""
    logger.info("Mock document fetch: %s", url)
    await asyncio.sleep(0.3)
    return {
        "url": url,
        "title": f"Document from {url}",
        "content": (
            f"This is mock content fetched from {url}. "
            "It contains information relevant to the query."
        ),
        "word_count": 42,
    }


@tool("calculator")
def calculator(expression: str) -> dict:
    """Evaluate a mathematical expression. Example: '2 + 3 * 4'."""
    logger.info("Mock calculator: %s", expression)
    return {"expression": expression, "result": _safe_eval(expression)}


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
    raise ValueError(f"Unsupported expression node: {type(node).__name__}")


if __name__ == "__main__":
    Runner(
        "demo-runner",
        kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
        api_key="your-secret-token",
    ).run()
