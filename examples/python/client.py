"""Interactive CLI client for Rebuno.

It creates an execution, streams the kernel's event log, and resolves any
human-in-the-loop approval the policy raises.
"""
import argparse
import asyncio
import json
import sys

from rebuno import Client
from rebuno.types import ExecutionStatus

KNOWN_AGENTS = ["researcher", "shell", "mcp", "hello"]


def dim(t: str) -> str:
    return f"\033[90m{t}\033[0m"


def bold(t: str) -> str:
    return f"\033[1m{t}\033[0m"


def green(t: str) -> str:
    return f"\033[32m{t}\033[0m"


def red(t: str) -> str:
    return f"\033[31m{t}\033[0m"


def yellow(t: str) -> str:
    return f"\033[33m{t}\033[0m"


async def prompt(text: str) -> str:
    return await asyncio.to_thread(input, text)


async def select_agent() -> str:
    print()
    for i, aid in enumerate(KNOWN_AGENTS, 1):
        print(f"  {dim(f'[{i}]')} {aid}")
    print(f"  {dim(f'[{len(KNOWN_AGENTS) + 1}]')} Enter custom agent id")
    print()
    while True:
        choice = (await prompt(f"  {bold('Select agent')}: ")).strip()
        if choice.isdigit():
            idx = int(choice)
            if 1 <= idx <= len(KNOWN_AGENTS):
                return KNOWN_AGENTS[idx - 1]
            if idx == len(KNOWN_AGENTS) + 1:
                while True:
                    aid = (await prompt(f"  {bold('Agent id')}: ")).strip()
                    if aid:
                        return aid
                    print(red("  Agent id cannot be empty."))
        elif choice in KNOWN_AGENTS:
            return choice
        else:
            print(red("  Invalid selection."))


def print_event(evt) -> None:
    seq = str(evt.event_seq).rjust(3, "0")
    ts = evt.occurred_at.split("T")[-1][:12] if evt.occurred_at else ""
    parts = [dim(seq), dim(ts), evt.type]

    payload = evt.payload if isinstance(evt.payload, dict) else {}
    if evt.type == "step.proposed":
        if target := payload.get("target"):
            parts.append(dim(f"tool={target}"))
    elif evt.type == "step.awaiting_approval":
        parts.append(yellow(f"tool={payload.get('target', '')}"))
    elif evt.type == "execution.completed":
        out = payload.get("output")
        if isinstance(out, dict) and (answer := out.get("answer")):
            parts.append(dim(f"({len(str(answer))} chars)"))
    elif evt.type == "execution.failed":
        if reason := payload.get("reason"):
            parts.append(red(str(reason)[:80]))

    print(f"  {'  '.join(parts)}")


async def handle_approval(client: Client, approval_id: str, target: str) -> None:
    print()
    print(f"  {yellow(bold('Approval required'))}  {dim('tool:')} {target or '?'}")
    print()
    while True:
        choice = (await prompt(f"  {bold('Approve? [y/n]')}: ")).strip().lower()
        if choice in ("y", "yes"):
            await client.grant_approval(approval_id, decided_by="cli-user")
            break
        if choice in ("n", "no"):
            await client.deny_approval(approval_id, decided_by="cli-user", rationale="denied by user")
            break
        print(red("  Please enter y or n."))
    print()


async def run_execution(client: Client, agent_id: str, query: str) -> None:
    execution = await client.create(agent_id, input={"query": query})
    print(f"\n  {dim('execution')} {execution.id}\n")

    after_seq = 0
    last_awaiting_target = ""
    terminal = {"execution.completed", "execution.failed", "execution.cancelled"}
    while True:
        events = await client.events(execution.id, after_seq=after_seq)
        for evt in events:
            after_seq = evt.event_seq
            print_event(evt)
            if evt.type == "step.awaiting_approval" and isinstance(evt.payload, dict):
                last_awaiting_target = evt.payload.get("target", "")
            elif evt.type == "approval.requested" and isinstance(evt.payload, dict):
                await handle_approval(
                    client, evt.payload.get("approval_id", ""), last_awaiting_target
                )
            elif evt.type in terminal:
                events = None
                break
        if events is None:
            break
        await asyncio.sleep(0.5)

    final = await client.get(execution.id)
    print()
    if final.status == ExecutionStatus.COMPLETED and final.output is not None:
        out = final.output
        answer = out.get("answer", out) if isinstance(out, dict) else out
        print(f"  {green(bold('Result:'))}")
        print(f"  {answer if isinstance(answer, str) else json.dumps(answer, indent=2)}")
    elif final.status == ExecutionStatus.FAILED:
        print(f"  {red(bold('Failed'))} {dim(final.failure_reason)}")
    elif final.status == ExecutionStatus.CANCELLED:
        print(f"  {dim(bold('Cancelled'))}")
    print()


async def amain(url: str, api_key: str, agent_id: str) -> None:
    async with Client(base_url=url, api_key=api_key) as client:
        print(f"\n  {bold('rebuno')} {dim('client')}  {dim(url)}")
        if not agent_id:
            agent_id = await select_agent()
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
                agent_id = await select_agent()
                print(f"\n  {dim('agent')} {bold(agent_id)}\n")
                continue
            try:
                await run_execution(client, agent_id, query)
            except Exception as e:
                print(f"\n  {red('Error:')} {e}\n")


def main() -> None:
    parser = argparse.ArgumentParser(description="Rebuno CLI client")
    parser.add_argument("--url", default="http://localhost:8080", help="Kernel URL")
    parser.add_argument("--api-key", default="", help="Bearer token (if the kernel requires one)")
    parser.add_argument("--agent", default="", help="Agent id (skips the picker)")
    args = parser.parse_args()
    try:
        asyncio.run(amain(args.url, args.api_key, args.agent))
    except KeyboardInterrupt:
        sys.exit(0)


if __name__ == "__main__":
    main()
