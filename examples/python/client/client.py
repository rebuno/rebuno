"""Interactive CLI client for Rebuno.

Usage:
    pip install rebuno
    python examples/client/client.py [--url http://localhost:8080]
"""

import argparse
import json
import sys

from rebuno import RebunoClient, ExecutionStatus


STATUS_ICONS = {
    "pending": "\033[33m●\033[0m",    # yellow
    "running": "\033[36m●\033[0m",    # cyan
    "blocked": "\033[35m●\033[0m",    # magenta
    "completed": "\033[32m●\033[0m",  # green
    "failed": "\033[31m●\033[0m",     # red
    "cancelled": "\033[90m●\033[0m",  # gray
}


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


def select_agent(client: RebunoClient) -> str:
    """Let the user pick an agent or type a custom ID."""
    known_agents: list[str] = []
    try:
        data = client.list_executions(limit=100)
        for ex in data.executions:
            aid = ex.agent_id
            if aid and aid not in known_agents:
                known_agents.append(aid)
    except Exception:
        pass

    if not known_agents:
        print(dim("  No agents found from previous executions."))
        while True:
            agent_id = input(f"  {bold('Agent ID')}: ").strip()
            if agent_id:
                return agent_id
            print(red("  Agent ID cannot be empty."))

    print()
    for i, aid in enumerate(known_agents, 1):
        print(f"  {dim(f'[{i}]')} {aid}")
    print(f"  {dim(f'[{len(known_agents) + 1}]')} Enter custom agent ID")
    print()

    while True:
        choice = input(f"  {bold('Select agent')}: ").strip()
        if choice.isdigit():
            idx = int(choice)
            if 1 <= idx <= len(known_agents):
                return known_agents[idx - 1]
            if idx == len(known_agents) + 1:
                while True:
                    agent_id = input(f"  {bold('Agent ID')}: ").strip()
                    if agent_id:
                        return agent_id
                    print(red("  Agent ID cannot be empty."))
        # Also accept typing the agent ID directly
        if choice in known_agents:
            return choice
        print(red("  Invalid selection."))


def print_event(evt) -> None:
    seq = str(evt.sequence).rjust(3, "0")
    etype = evt.type
    ts = ""
    if evt.timestamp:
        ts = str(evt.timestamp).split("T")[-1][:12]

    parts = [dim(seq), dim(ts), etype]

    payload = evt.payload or {}
    if etype == "step.created" and isinstance(payload, dict):
        tool = payload.get("tool_id", "")
        if tool:
            parts.append(dim(f"tool={tool}"))
    elif etype == "execution.completed" and isinstance(payload, dict):
        output = payload.get("output")
        if output and isinstance(output, dict):
            answer = output.get("answer", "")
            if answer:
                parts.append(dim(f"({len(answer)} chars)"))
    elif etype == "execution.failed" and isinstance(payload, dict):
        error = payload.get("error", "")
        if error:
            parts.append(red(error[:80]))
    elif etype == "step.approval_required" and isinstance(payload, dict):
        tool = payload.get("tool_id", "")
        reason = payload.get("reason", "")
        parts.append(yellow(f"tool={tool}"))
        if reason:
            parts.append(dim(reason))
    elif etype == "execution.blocked" and isinstance(payload, dict):
        reason = payload.get("reason", "")
        if reason == "approval":
            tool = payload.get("tool_id", "")
            parts.append(yellow(f"awaiting approval: {tool}"))

    print(f"  {'  '.join(parts)}")


TERMINAL_EVENTS = {"execution.completed", "execution.failed", "execution.cancelled"}


def run_execution(client: RebunoClient, agent_id: str, query: str) -> None:
    result = client.create_execution(
        agent_id=agent_id,
        input={"query": query},
    )
    execution_id = result.id
    print(f"\n  {dim('execution')} {execution_id}")
    print()

    for event in client.stream_events(execution_id):
        print_event(event)
        if event.type in TERMINAL_EVENTS:
            break
        if (
            event.type == "execution.blocked"
            and isinstance(event.payload, dict)
            and event.payload.get("reason") == "approval"
        ):
            step_id = event.payload.get("ref", "")
            tool_id = event.payload.get("tool_id", "")
            args = event.payload.get("arguments")
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
                choice = input(f"  {bold('Approve? [y/n]')}: ").strip().lower()
                if choice in ("y", "yes"):
                    approved = True
                    break
                if choice in ("n", "no"):
                    approved = False
                    break
                print(red("  Please enter y or n."))
            payload = {"step_id": step_id, "approved": approved}
            if not approved:
                payload["reason"] = "denied by user"
            try:
                client.send_signal(
                    execution_id=execution_id,
                    signal_type="step.approve",
                    payload=payload,
                )
            except Exception as e:
                print(f"  {red('Failed to send approval signal:')} {e}")
            print()

    # Fetch final execution state for output display.
    execution = client.get_execution(execution_id)

    print()
    if execution.status == ExecutionStatus.COMPLETED and execution.output:
        output = execution.output
        if isinstance(output, dict):
            answer = output.get("answer", str(output))
        else:
            answer = str(output)
        print(f"  {green(bold('Result:'))}")
        for line in answer.split("\n"):
            while len(line) > 80:
                cut = line[:80].rfind(" ")
                if cut <= 0:
                    cut = 80
                print(f"  {line[:cut]}")
                line = line[cut:].lstrip()
            print(f"  {line}")
    elif execution.status == ExecutionStatus.FAILED:
        print(f"  {red(bold('Failed'))}")
    elif execution.status == ExecutionStatus.CANCELLED:
        print(f"  {dim(bold('Cancelled'))}")
    print()


def main() -> None:
    parser = argparse.ArgumentParser(description="Rebuno CLI client")
    parser.add_argument(
        "--url",
        default="http://localhost:8080",
        help="Kernel URL (default: http://localhost:8080)",
    )
    parser.add_argument(
        "--api-key",
        default="",
        help="Bearer token for authentication",
    )
    args = parser.parse_args()

    client = RebunoClient(base_url=args.url, api_key=args.api_key)

    try:
        client.health()
    except Exception as e:
        print(f"\n  {red('Cannot connect to kernel at')} {args.url}")
        print(f"  {dim(str(e))}\n")
        sys.exit(1)

    print(f"\n  {bold('rebuno')} {dim('client')}")
    print(f"  {dim(args.url)}")
    print()

    agent_id = select_agent(client)
    print(f"\n  {dim('agent')} {bold(agent_id)}\n")

    while True:
        try:
            query = input(f"  {bold('Query >')} ").strip()
        except (KeyboardInterrupt, EOFError):
            print(f"\n\n  {dim('bye')}\n")
            break

        if not query:
            continue
        if query.lower() in ("exit", "quit", ":q"):
            print(f"\n  {dim('bye')}\n")
            break
        if query.lower() == "/agent":
            agent_id = select_agent(client)
            print(f"\n  {dim('agent')} {bold(agent_id)}\n")
            continue

        run_execution(client, agent_id, query)

    client.close()


if __name__ == "__main__":
    main()
