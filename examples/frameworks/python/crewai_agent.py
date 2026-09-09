import os
import uuid

from crewai import LLM, Crew, Task
from crewai import Agent as CrewAgent
from crewai.tools import tool as crewai_tool
from rebuno import Agent, execution, step
from support import TOOLS

GATEWAY_URL = os.environ["GATEWAY_URL"]
GATEWAY_KEY = os.environ["GATEWAY_KEY"]
SECRET = os.environ["REBUNO_AGENT_SECRET"]


async def process(query: str) -> dict:
    ref = await step("brief_ref", lambda: uuid.uuid4().hex[:8])
    ctx = execution()
    llm = LLM(
        model=os.environ["LLM_MODEL"],
        base_url=GATEWAY_URL,  # CrewAI builds its own HTTP client, so base_url points to a Rebuno compatible gateway
        api_key=GATEWAY_KEY,
        extra_headers={
            "rebuno-execution-id": ctx.id,
            "rebuno-dispatch-id": ctx.dispatch_id,
            "rebuno-dispatch-attempt": str(ctx.dispatch_attempt),
            "rebuno-agent-id": ctx.agent_id,
            "rebuno-agent-secret": SECRET,
        },
    )
    writer = CrewAgent(
        role="support specialist",
        goal=query,
        backstory="You investigate customer issues, create tickets, and email support summaries.",
        llm=llm,
        tools=[crewai_tool(fn.__name__)(fn) for fn in TOOLS],
    )
    task = Task(
        description=f"[{ref}] {query}", expected_output="a short brief", agent=writer
    )
    result = await Crew(agents=[writer], tasks=[task]).kickoff_async()
    return {"answer": str(result), "ref": ref}


agent = Agent(os.environ.get("AGENT_ID", "support-py-crewai"))

if __name__ == "__main__":
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
