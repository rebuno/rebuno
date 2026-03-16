import asyncio
import logging
import os

from rebuno import AsyncBaseAgent, AsyncAgentContext

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("hello-agent")

class HelloAgent(AsyncBaseAgent):
    async def process(self, ctx: AsyncAgentContext) -> dict:
        query = ctx.input.get("query", "") if isinstance(ctx.input, dict) else str(ctx.input)
        logger.info("Processing query: %s", query)
        reverse_result = await ctx.invoke_tool("reverse", {"text": query})
        count_result = await ctx.invoke_tool("word_count", {"text": query})

        logger.info(
            "Agent finished: reverse_result=%s, count_result=%s",
            reverse_result.get("reversed", ""),
            count_result.get("count", 0),
        )
        return {
            "query": query,
            "reversed": reverse_result.get("reversed", ""),
            "word_count": count_result.get("count", 0),
        }


agent = HelloAgent(
    agent_id="hello",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
)


@agent.tool("reverse")
def reverse(text: str) -> dict:
    """Reverse a string."""
    return {"reversed": text[::-1]}


@agent.tool("word_count")
def word_count(text: str) -> dict:
    """Count words in a string."""
    return {"count": len(text.split())}


if __name__ == "__main__":
    logger.info("Starting hello agent")
    asyncio.run(agent.run())
