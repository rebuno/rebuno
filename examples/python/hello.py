import logging
import os

from rebuno import Agent, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("hello")


@tool
async def reverse(text: str) -> dict:
    """Reverse a string."""
    return {"reversed": text[::-1]}


@tool
async def word_count(text: str) -> dict:
    """Count words in a string."""
    return {"count": len(text.split())}

async def process(query: str) -> dict:
    reversed_result = await reverse(text=query)
    count_result = await word_count(text=query)
    logger.info(
        "reversed=%s count=%s",
        reversed_result["reversed"], count_result["count"],
    )
    return {
        "query": query,
        "reversed": reversed_result["reversed"],
        "word_count": count_result["count"],
    }


if __name__ == "__main__":
    agent = Agent(
        "hello",
        secret=os.environ.get("REBUNO_AGENT_SECRET", "hello-secret"),
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    logger.info("hello agent listening on :5000")
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
