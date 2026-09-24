import inspect
import json
import os
import uuid

from claude_agent_sdk import (
    ClaudeAgentOptions,
    ResultMessage,
    create_sdk_mcp_server,
)
from claude_agent_sdk import query as claude_query
from claude_agent_sdk import tool as claude_tool
from rebuno import Agent, execution, step
from support import TOOLS

GATEWAY_URL = os.environ["GATEWAY_URL"]
GATEWAY_KEY = os.environ["GATEWAY_KEY"]
SECRET = os.environ["REBUNO_AGENT_SECRET"]


def mcp_tool(fn):
    params = {name: str for name in inspect.signature(fn).parameters}

    @claude_tool(fn.__name__, fn.__doc__, params)
    async def call(args):
        result = await fn(**args)
        text = result if isinstance(result, str) else json.dumps(result)
        return {"content": [{"type": "text", "text": text}]}

    return call


SUPPORT = create_sdk_mcp_server("support", tools=[mcp_tool(fn) for fn in TOOLS])


async def process(query: str) -> dict:
    ref = await step("brief_ref", lambda: uuid.uuid4().hex[:8])
    ctx = execution()
    lease = {
        "rebuno-execution-id": ctx.id,
        "rebuno-dispatch-id": ctx.dispatch_id,
        "rebuno-dispatch-attempt": str(ctx.dispatch_attempt),
        "rebuno-agent-id": ctx.agent_id,
        "rebuno-agent-secret": SECRET,
    }
    options = ClaudeAgentOptions(
        model=os.environ["LLM_MODEL"],
        system_prompt="You investigate customer issues, create tickets, and email support summaries.",
        # Built-in tools run inside the CLI, out of the kernel's sight, so only
        # the Rebuno-routed MCP tools are exposed.
        tools=[],
        mcp_servers={"support": SUPPORT},
        allowed_tools=[f"mcp__support__{fn.__name__}" for fn in TOOLS],
        setting_sources=[],
        # The CLI makes the model calls from its own process, so they reach the
        # kernel through a Rebuno-compatible gateway rather than http_client().
        env={
            "ANTHROPIC_BASE_URL": GATEWAY_URL,
            "ANTHROPIC_AUTH_TOKEN": GATEWAY_KEY,
            "ANTHROPIC_CUSTOM_HEADERS": "\n".join(
                f"{k}: {v}" for k, v in lease.items()
            ),
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
        },
    )
    answer = None
    async for message in claude_query(prompt=f"[{ref}] {query}", options=options):
        if isinstance(message, ResultMessage):
            if message.is_error:
                raise RuntimeError(message.result or message.subtype)
            answer = message.result
    return {"answer": answer, "ref": ref}


agent = Agent(os.environ.get("AGENT_ID", "support-py-claude-agent-sdk"))

if __name__ == "__main__":
    agent.run(process, port=int(os.environ.get("AGENT_PORT", "5000")))
