"""Demo agent: LangGraph agent with shell execution (demonstrates approval flow).

The shell_exec tool is governed by policy:
  - Safe commands (ls, cat, pwd, ...) are auto-allowed by the agent policy
  - Other commands require human approval via the client
  - The global policy denies shell_exec for any agent without explicit rules
"""

import asyncio
import logging
import os

from langchain.agents import create_agent
from langchain_openai import ChatOpenAI

from rebuno import Agent, tool

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
)
logger = logging.getLogger("shell")

SYSTEM_PROMPT = (
    "You are a helpful sysadmin assistant. You can run shell commands to answer "
    "questions about the system. Use the shell tool to execute commands. Start with "
    "safe read-only commands when possible."
)

MODEL = os.environ.get("OPENAI_MODEL", "gpt-4o-mini")


@tool("shell_exec", idempotency="at_most_once")
async def shell_exec(command: str, timeout: int = 30) -> dict:
    """Execute a shell command."""
    logger.info("executing: %s", command)
    proc = await asyncio.create_subprocess_shell(
        command,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    try:
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
    except asyncio.TimeoutError:
        proc.kill()
        return {"exit_code": -1, "stdout": "", "stderr": "Command timed out"}
    return {
        "exit_code": proc.returncode,
        "stdout": stdout.decode(errors="replace"),
        "stderr": stderr.decode(errors="replace"),
    }


async def process(query: str) -> dict:
    llm = ChatOpenAI(model=MODEL, temperature=0)
    graph = create_agent(model=llm, tools=[shell_exec], system_prompt=SYSTEM_PROMPT)
    result = await graph.ainvoke({"messages": [{"role": "user", "content": query}]})
    answer = result["messages"][-1].content
    logger.info("done: %s", answer)
    return {"query": query, "answer": answer}


if __name__ == "__main__":
    agent = Agent(
        "shell",
        secret=os.environ.get("REBUNO_AGENT_SECRET", "shell-secret"),
        kernel_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    logger.info("shell agent listening on :5002")
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5002")))
