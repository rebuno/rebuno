import logging
import os

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

from rebuno import Agent
from rebuno.mcp import wrap_mcp_tools

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("mcp")

SYSTEM_PROMPT = (
    "You are a helpful assistant with filesystem access. Use the filesystem tools "
    "to list directories and read files, then answer the user's question."
)

MODEL = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")
MCP_ROOT = os.environ.get("MCP_ROOT", "/tmp")

filesystem = StdioServerParameters(
    command="npx",
    args=["-y", "@modelcontextprotocol/server-filesystem", MCP_ROOT],
)


async def process(query: str) -> dict:
    async with stdio_client(filesystem) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            listed = await session.list_tools()
            tools = wrap_mcp_tools(
                listed.tools,
                call=lambda name, args: session.call_tool(name, args),
                prefix="fs",
            )
            logger.info("MCP tools: %s", [t.__name__ for t in tools])

            llm = ChatOpenAI(model=MODEL, temperature=0)
            graph = create_agent(model=llm, tools=tools, system_prompt=SYSTEM_PROMPT)
            result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
            answer = result["messages"][-1].content
            logger.info("done: %s", answer)
            return {"query": query, "answer": answer}


if __name__ == "__main__":
    agent = Agent(
        "mcp",
        secret=os.environ.get("REBUNO_AGENT_SECRET", "mcp-secret"),
        kernel_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    logger.info("mcp agent listening on :5003")
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5003")))
