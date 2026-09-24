import os

from langchain.agents import create_agent
from langchain_mcp_adapters.client import MultiServerMCPClient
from langchain_openai import ChatOpenAI
from rebuno import Agent, http_client, tool, wrap_tool

SYSTEM_PROMPT = """\
Use executor_search to find tools from connected integrations, then
executor_invoke to call one: pass the result's id string as `tool` and
arguments matching its input schema as `arguments`. Search with a few keywords
naming the API method, without ids or names; if a search finds nothing useful,
try other words. Some calls wait for human approval and some are refused by
policy; report what happened."""

executor = MultiServerMCPClient(
    {
        "executor": {
            "transport": "streamable_http",
            "url": os.environ["EXECUTOR_URL"] + "?mode=passthrough",
            "headers": {"Authorization": f"Bearer {os.environ['EXECUTOR_API_KEY']}"},
        }
    }
)


async def process(query: str) -> dict:
    mcp = {t.name: t for t in await executor.get_tools()}

    @tool("executor_search")
    async def search(query: str) -> str:
        """Search the connected integrations for tools matching a short description."""
        return await mcp["search"].ainvoke({"query": query})

    invoke = wrap_tool(
        "executor_invoke",
        mcp["invoke"].ainvoke,
        description=mcp["invoke"].description,
        args_schema=mcp["invoke"].args_schema,
        idempotency="at_most_once",
    )

    llm = ChatOpenAI(
        model=os.environ["LLM_MODEL"],
        base_url=os.environ["LLM_BASE_URL"],
        api_key=os.environ["LLM_API_KEY"],
        http_async_client=http_client(),
        streaming=True,
        reasoning_effort="none",
    )
    graph = create_agent(model=llm, tools=[search, invoke], system_prompt=SYSTEM_PROMPT)
    result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
    return {"query": query, "answer": result["messages"][-1].text}


if __name__ == "__main__":
    agent = Agent(
        "executor",
        secret="executor-secret",
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    agent.run(process, port=5000)
