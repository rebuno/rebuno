"""Interactive CLI client for Rebuno.

Usage:
    pip install rebuno
    python examples/client/client.py [--url http://localhost:8080]
"""

import argparse
import asyncio
import json
import sys

from rebuno import Client
from rebuno.types import ExecutionStatus


def dim(text: str) -> str:
    return f"\033[90m{text}\033[0m"


def bold(text: str) -> str:
    return f"\033[1m{text}\033[0m"


def green(text: str) -> str:
    return f"\033[32m{text}\033[0m"


def red(text: str) -> str:
    return f"\033[31m{text}\033[0m"


def yellow(text: str) -> str:
    return f"\033[33m{text}\033[0m"


async def prompt(text: str) -> str:
    """Read a line from stdin without blocking the event loop."""
    return await asyncio.to_thread(input, text)


async def select_agent(client: Client) -> str:
    """Let the user pick an agent or type a custom ID."""
    known: list[str] = []
    try:
        data = await client.list(limit=100)
        for ex in data.executions:
            if ex.agent_id and ex.agent_id not in known:
                known.append(ex.agent_id)
    except Exception:
        pass

    if not known:
        print(dim("  No agents found from previous executions."))
        while True:
            agent_id = (await prompt(f"  {bold('Agent ID')}: ")).strip()
            if agent_id:
                return agent_id
            print(red("  Agent ID cannot be empty."))

    print()
    for i, aid in enumerate(known, 1):
        print(f"  {dim(f'[{i}]')} {aid}")
    print(f"  {dim(f'[{len(known) + 1}]')} Enter custom agent ID")
    print()

    while True:
        choice = (await prompt(f"  {bold('Select agent')}: ")).strip()
        if choice.isdigit():
            idx = int(choice)
            if 1 <= idx <= len(known):
                return known[idx - 1]
            if idx == len(known) + 1:
                while True:
                    agent_id = (await prompt(f"  {bold('Agent ID')}: ")).strip()
                    if agent_id:
                        return agent_id
                    print(red("  Agent ID cannot be empty."))
        if choice in known:
            return choice
        print(red("  Invalid selection."))


def print_event(evt) -> None:
    seq = str(evt.sequence).rjust(3, "0")
    ts = ""
    if evt.timestamp:
        ts = str(evt.timestamp).split("T")[-1][:12]

    parts = [dim(seq), dim(ts), evt.type]

    payload = evt.payload or {}
    if evt.type == "step.created" and isinstance(payload, dict):
        if tool_id := payload.get("tool_id"):
            parts.append(dim(f"tool={tool_id}"))
    elif evt.type == "execution.completed" and isinstance(payload, dict):
        output = payload.get("output")
        if isinstance(output, dict):
            answer = output.get("answer", "")
            if answer:
                parts.append(dim(f"({len(answer)} chars)"))
    elif evt.type == "execution.failed" and isinstance(payload, dict):
        if error := payload.get("error"):
            parts.append(red(error[:80]))
    elif evt.type == "step.approval_required" and isinstance(payload, dict):
        parts.append(yellow(f"tool={payload.get('tool_id', '')}"))
        if reason := payload.get("reason"):
            parts.append(dim(reason))
    elif evt.type == "execution.blocked" and isinstance(payload, dict):
        if payload.get("reason") == "approval":
            parts.append(yellow(f"awaiting approval: {payload.get('tool_id', '')}"))

    print(f"  {'  '.join(parts)}")


async def run_execution(client: Client, agent_id: str, query: str) -> None:
    execution = await client.create(agent_id=agent_id, input={"query": query})
    print(f"\n  {dim('execution')} {execution.id}")
    print()

    async for event in client.events(execution.id):
        print_event(event)
        if event.type in ("execution.completed", "execution.failed", "execution.cancelled"):
            break
        if (
            event.type == "execution.blocked"
            and isinstance(event.payload, dict)
            and event.payload.get("reason") == "approval"
        ):
            await _handle_approval(client, execution.id, event.payload)

    final = await client.get(execution.id)

    print()
    if final.status == ExecutionStatus.COMPLETED and final.output:
        output = final.output
        answer = output.get("answer", str(output)) if isinstance(output, dict) else str(output)
        print(f"  {green(bold('Result:'))}")
        for line in answer.split("\n"):
            while len(line) > 80:
                cut = line[:80].rfind(" ")
                if cut <= 0:
                    cut = 80
                print(f"  {line[:cut]}")
                line = line[cut:].lstrip()
            print(f"  {line}")
    elif final.status == ExecutionStatus.FAILED:
        print(f"  {red(bold('Failed'))}")
    elif final.status == ExecutionStatus.CANCELLED:
        print(f"  {dim(bold('Cancelled'))}")
    print()


async def _handle_approval(client: Client, execution_id: str, payload: dict) -> None:
    step_id = payload.get("ref", "")
    tool_id = payload.get("tool_id", "")
    args = payload.get("arguments")

    print()
    print(f"  {yellow(bold('Approval required'))}")
    print(f"  {dim('tool:')} {tool_id}")
    if args:
        try:
            formatted = json.dumps(args, indent=2) if isinstance(args, dict) else str(args)
        except Exception:
            formatted = str(args)
        for line in formatted.split("\n"):
            print(f"  {dim('  ' + line)}")
    print()

    while True:
        choice = (await prompt(f"  {bold('Approve? [y/n]')}: ")).strip().lower()
        if choice in ("y", "yes"):
            approved = True
            break
        if choice in ("n", "no"):
            approved = False
            break
        print(red("  Please enter y or n."))

    signal_payload = {"step_id": step_id, "approved": approved}
    if not approved:
        signal_payload["reason"] = "denied by user"
    try:
        await client.send_signal(execution_id, "step.approve", payload=signal_payload)
    except Exception as e:
        print(f"  {red('Failed to send approval signal:')} {e}")
    print()


async def amain(url: str, api_key: str) -> None:
    async with Client(base_url=url, api_key=api_key) as client:
        try:
            await client.health()
        except Exception as e:
            print(f"\n  {red('Cannot connect to kernel at')} {url}")
            print(f"  {dim(str(e))}\n")
            sys.exit(1)

        print(f"\n  {bold('rebuno')} {dim('client')}")
        print(f"  {dim(url)}")
        print()

        agent_id = await select_agent(client)
        print(f"\n  {dim('agent')} {bold(agent_id)}\n")

        while True:
            try:
                query = (await prompt(f"  {bold('Query >')} ")).strip()
            except (KeyboardInterrupt, EOFError):
                print(f"\n\n  {dim('bye')}\n")
                return

            if not query:
                continue
            if query.lower() in ("exit", "quit", ":q"):
                print(f"\n  {dim('bye')}\n")
                return
            if query.lower() == "/agent":
                agent_id = await select_agent(client)
                print(f"\n  {dim('agent')} {bold(agent_id)}\n")
                continue

            await run_execution(client, agent_id, query)


def main() -> None:
    parser = argparse.ArgumentParser(description="Rebuno CLI client")
    parser.add_argument("--url", default="http://localhost:8080",
                        help="Kernel URL (default: http://localhost:8080)")
    parser.add_argument("--api-key", default="", help="Bearer token for authentication")
    args = parser.parse_args()
    asyncio.run(amain(args.url, args.api_key))


if __name__ == "__main__":
    main()
