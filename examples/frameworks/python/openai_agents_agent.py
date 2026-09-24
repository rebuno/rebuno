import os
import uuid

from agents import Agent as OpenAIAgent
from agents import ModelSettings, OpenAIResponsesModel, RunConfig, Runner, function_tool
from openai import AsyncOpenAI
from rebuno import Agent, execution, http_client, step
from support import TOOLS


async def process(query: str) -> dict:
    ref = await step("brief_ref", lambda: uuid.uuid4().hex[:8])
    client = AsyncOpenAI(http_client=http_client())
    writer = OpenAIAgent(
        name="support specialist",
        model=OpenAIResponsesModel(os.environ["LLM_MODEL"], client),
        # Without a failure function a Blocked tool unwinds the run instead of
        # being handed to the model as an error message.
        tools=[function_tool(fn, failure_error_function=None) for fn in TOOLS],
    )
    # The Runner generates a random prompt_cache_key per run for api.openai.com,
    # which would give every model call a new step identity on resume.
    config = RunConfig(
        model_settings=ModelSettings(
            extra_args={"prompt_cache_key": str(execution().id)}
        )
    )
    result = await Runner.run(writer, f"[{ref}] {query}", run_config=config)
    return {"answer": result.final_output, "ref": ref}


agent = Agent(os.environ.get("AGENT_ID", "support-py-openai-agents"))

if __name__ == "__main__":
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
