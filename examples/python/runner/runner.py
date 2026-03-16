import ast
import asyncio
import logging
import operator
import os
from typing import Any

from rebuno import AsyncBaseRunner

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
logger = logging.getLogger("demo-runner")


class DemoRunner(AsyncBaseRunner):
    async def execute(self, tool_id: str, arguments: Any) -> Any:
        if tool_id == "web.search":
            return await self._web_search(arguments)
        elif tool_id == "doc.fetch":
            return await self._doc_fetch(arguments)
        elif tool_id == "calculator":
            return self._calculator(arguments)
        else:
            raise ValueError(f"Unknown tool: {tool_id}")

    async def _web_search(self, arguments: Any) -> dict:
        query = arguments.get("query", "") if isinstance(arguments, dict) else ""
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

    async def _doc_fetch(self, arguments: Any) -> dict:
        url = arguments.get("url", "") if isinstance(arguments, dict) else ""
        logger.info("Mock document fetch: %s", url)
        await asyncio.sleep(0.3)
        return {
            "url": url,
            "title": f"Document from {url}",
            "content": f"This is mock content fetched from {url}. "
            "It contains information relevant to the query.",
            "word_count": 42,
        }

    def _calculator(self, arguments: Any) -> dict:
        expression = arguments.get("expression", "") if isinstance(arguments, dict) else ""
        logger.info("Mock calculator: %s", expression)
        try:
            result = _safe_eval(expression)
            return {"expression": expression, "result": result}
        except Exception as e:
            raise ValueError(f"Calculator error: {e}")


_OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.USub: operator.neg,
    ast.UAdd: operator.pos,
}


def _safe_eval(expr: str) -> float:
    """Evaluate a simple arithmetic expression without eval()."""
    tree = ast.parse(expr, mode="eval")
    return _eval_node(tree.body)


def _eval_node(node: ast.AST) -> float:
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return node.value
    if isinstance(node, ast.BinOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.left), _eval_node(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in _OPS:
        return _OPS[type(node.op)](_eval_node(node.operand))
    raise ValueError(f"Unsupported expression node: {type(node).__name__}")


if __name__ == "__main__":
    runner = DemoRunner(
        runner_id="demo-runner",
        kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
        capabilities=["web.search", "doc.fetch", "calculator"],
        name="Demo Runner",
        api_key="your-secret-token",
    )
    asyncio.run(runner.run())
