import logging
import os

from rebuno import Agent, execution, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("hello-agent")

agent = Agent(
    "hello",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8001"),
)


@tool("reverse")
def reverse(text: str) -> dict:
    """Reverse a string."""
    return {"reversed": text[::-1]}


@tool("word_count")
def word_count(text: str) -> dict:
    """Count words in a string."""
    return {"count": len(text.split())}


async def process(query: str) -> dict:
    logger.info("Processing query: %s (execution=%s)", query, execution.id)
    reversed_result = await reverse(text=query)
    count_result = await word_count(text=query)

    logger.info(
        "Agent finished: reversed=%s count=%s",
        reversed_result["reversed"], count_result["count"],
    )
    return {
        "query": query,
        "reversed": reversed_result["reversed"],
        "word_count": count_result["count"],
    }


if __name__ == "__main__":
    logger.info("Starting hello agent")
    agent.run(process)
