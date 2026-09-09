import os
import uuid

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI
from rebuno import Agent, http_client, step
from support import TOOLS


async def process(query: str) -> dict:
    ref = await step("brief_ref", lambda: uuid.uuid4().hex[:8])
    llm = ChatOpenAI(
        model=os.environ["LLM_MODEL"],
        http_async_client=http_client(),
        streaming=os.environ.get("LLM_STREAM") == "1",
    )
    graph = create_agent(model=llm, tools=TOOLS)
    result = await graph.ainvoke(
        {"messages": [{"role": "user", "content": f"[{ref}] {query}"}]}
    )
    return {"answer": result["messages"][-1].content, "ref": ref}


agent = Agent(os.environ.get("AGENT_ID", "support-py-langchain"))

if __name__ == "__main__":
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
