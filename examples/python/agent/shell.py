"""Demo agent: LangGraph agent with shell execution (demonstrates approval flow).

The shell.exec tool is governed by policy:
  - Safe commands (ls, cat, pwd, ...) are auto-allowed by the agent policy
  - Other commands require human approval via the client
  - The global policy denies shell.exec for any agent without explicit rules
"""

import asyncio
import logging
import os
from typing import Any

from langchain_openai import ChatOpenAI
from langchain.agents import create_agent

from rebuno import AsyncAgentContext, AsyncBaseAgent

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("demo-agent-shell")

SYSTEM_PROMPT = (
    "You are a helpful sysadmin assistant. You can run shell commands to answer "
    "questions about the system. Use the shell tool to execute commands. Start with "
    "safe read-only commands when possible."
)


class ShellAgent(AsyncBaseAgent):
    """LangGraph agent that executes shell commands under kernel policy."""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self._model = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")

    async def process(self, ctx: AsyncAgentContext) -> dict:
        query = ""
        if isinstance(ctx.input, dict):
            query = ctx.input.get("query", "")
        elif isinstance(ctx.input, str):
            query = ctx.input

        if not query:
            return {"error": "No query provided"}

        logger.info("Processing query: %s", query)

        tools = ctx.get_tools()

        llm = ChatOpenAI(model=self._model, temperature=0)
        agent = create_agent(
            model=llm,
            tools=tools,
            system_prompt=SYSTEM_PROMPT,
        )

        result = await agent.ainvoke(
            {"messages": [{"role": "user", "content": query}]}
        )

        final_message = result["messages"][-1].content
        logger.info("Agent finished: %s", final_message)

        return {"query": query, "answer": final_message}


agent = ShellAgent(
    agent_id="shell-assistant",
    kernel_url=os.environ.get("REBUNO_KERNEL_URL", "http://localhost:8080"),
    api_key="your-secret-token",
)

@agent.tool("shell.exec")
async def shell_exec(command: str, timeout: int = 30) -> dict:
    """Execute a shell command."""
    logger.info("Executing: %s", command)
    try:
        proc = await asyncio.create_subprocess_shell(
            command,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(
            proc.communicate(), timeout=timeout
        )
        return {
            "exit_code": proc.returncode,
            "stdout": stdout.decode(errors="replace"),
            "stderr": stderr.decode(errors="replace"),
        }
    except asyncio.TimeoutError:
        proc.kill()
        return {"exit_code": -1, "stdout": "", "stderr": "Command timed out"}


if __name__ == "__main__":
    asyncio.run(agent.run())
