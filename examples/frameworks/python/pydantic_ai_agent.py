import os
import uuid

from openai import AsyncOpenAI
from pydantic_ai import Agent as PydanticAgent
from pydantic_ai.models.openai import OpenAIResponsesModel
from pydantic_ai.providers.openai import OpenAIProvider
from rebuno import Agent, http_client, step
from support import TOOLS


async def process(query: str) -> dict:
    ref = await step("brief_ref", lambda: uuid.uuid4().hex[:8])
    client = AsyncOpenAI(http_client=http_client())
    model = OpenAIResponsesModel(
        os.environ["LLM_MODEL"],
        provider=OpenAIProvider(openai_client=client),
    )
    result = await PydanticAgent(model, tools=TOOLS).run(f"[{ref}] {query}")
    return {"answer": result.output, "ref": ref}


agent = Agent(os.environ.get("AGENT_ID", "support-py-pydantic-ai"))

if __name__ == "__main__":
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
