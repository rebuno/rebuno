import base64
import json
import os
import re
from pathlib import Path

import httpx2
from e2b import (
    AsyncSandbox,
    CommandExitException,
    FileNotFoundException,
    SandboxQuery,
    SandboxState,
    TimeoutException,
)
from langchain.agents import create_agent
from langchain_core.messages import messages_from_dict, messages_to_dict
from langchain_openai import ChatOpenAI
from rebuno import Agent, Blocked, execution, http_client, step, tool

REPO = os.environ["REPO"]
WORKDIR = "/home/user/repo"
SESSIONS = Path(__file__).parent / "sessions"


def load_conversation(session: str) -> list:
    path = SESSIONS / f"{session}.json"
    return json.loads(path.read_text()) if path.exists() else []


def save_conversation(session: str, messages: list) -> None:
    SESSIONS.mkdir(exist_ok=True)
    (SESSIONS / f"{session}.json").write_text(json.dumps(messages))


async def find_sandbox(session: str) -> str:
    query = SandboxQuery(
        metadata={"session": session},
        state=[SandboxState.RUNNING, SandboxState.PAUSED],
    )
    found = await AsyncSandbox.list(query).next_items()
    if found:
        return found[0].sandbox_id
    sandbox = await AsyncSandbox.create(
        timeout=600, metadata={"session": session}, lifecycle={"on_timeout": "pause"}
    )
    return sandbox.sandbox_id


async def process(task: str, session: str | None = None) -> dict:
    session = session or execution().id[-12:]
    if not re.fullmatch(r"[\w-]+", session):
        raise ValueError("session must contain only letters, digits, - and _")
    branch = f"rebuno/{session}"

    history = await step("load_conversation", load_conversation, {"session": session})
    sandbox_id = await step("find_sandbox", find_sandbox, {"session": session})
    sandbox = await AsyncSandbox.connect(sandbox_id, timeout=600)

    token = os.environ["GITHUB_TOKEN"]
    credentials = base64.b64encode(f"x-access-token:{token}".encode()).decode()
    await sandbox.update_network(
        {
            "rules": {
                "github.com": [
                    {
                        "transform": {
                            "headers": {"Authorization": f"Basic {credentials}"}
                        }
                    }
                ]
            }
        }
    )
    await sandbox.commands.run(
        f"test -d {WORKDIR} || (git clone -q https://github.com/{REPO}.git {WORKDIR}"
        f" && cd {WORKDIR} && git checkout -q -b {branch}"
        " && git config user.name 'Rebuno Agent' && git config user.email agent@rebuno.io)",
        timeout=120,
    )

    @tool("shell")
    async def shell(command: str) -> str:
        """Run a shell command in the repository and return its exit code and output."""
        try:
            result = await sandbox.commands.run(command, cwd=WORKDIR, timeout=120)
        except CommandExitException as e:
            result = e
        except TimeoutException:
            return "timed out after 120 seconds"
        return f"exit code {result.exit_code}\n{result.stdout}{result.stderr}"[-10000:]

    @tool("read_file")
    async def read_file(path: str) -> str:
        """Return a file's contents. The path is relative to the repository root."""
        try:
            return await sandbox.files.read(f"{WORKDIR}/{path}")
        except FileNotFoundException:
            return f"{path} does not exist"

    @tool("write_file")
    async def write_file(path: str, content: str) -> str:
        """Replace a file's contents, creating it if needed. The path is relative to the repository root."""
        await sandbox.files.write(f"{WORKDIR}/{path}", content)
        return f"wrote {path}"

    @tool("open_pr", idempotency="at_most_once")
    async def open_pr(title: str, body: str) -> str:
        """Open a pull request from the pushed branch."""
        async with httpx2.AsyncClient(
            headers={"Authorization": f"Bearer {token}"}
        ) as github:
            repo = f"https://api.github.com/repos/{REPO}"
            base = (await github.get(repo)).json()["default_branch"]
            response = await github.post(
                f"{repo}/pulls",
                json={"title": title, "body": body, "head": branch, "base": base},
            )
        return response.json()["html_url"] if response.is_success else response.text

    llm = ChatOpenAI(
        model=os.environ["LLM_MODEL"],
        base_url=os.environ["LLM_BASE_URL"],
        api_key=os.environ["LLM_API_KEY"],
        http_async_client=http_client(),
        streaming=True,
    )
    graph = create_agent(
        model=llm,
        tools=[shell, read_file, write_file, open_pr],
        system_prompt=f"""\
You are working in a checkout of {REPO} on the branch {branch}. Use shell to run
commands in the repository, read_file to read a file, and write_file to replace
a file's contents. Run the tests after a change. When they pass, commit, push
the branch with `git push origin {branch}`, and call open_pr once with a title
and a short description. Later pushes to the branch update the same pull
request.""",
    )

    result = None
    try:
        result = await graph.ainvoke(
            {
                "messages": [
                    *messages_from_dict(history),
                    {"role": "user", "content": task},
                ]
            }
        )
    finally:
        if result is not None or isinstance(execution().suspension, Blocked):
            await sandbox.pause()

    messages = messages_to_dict(result["messages"])
    await step(
        "save_conversation",
        save_conversation,
        {"session": session, "messages": messages},
    )
    return {"session": session, "answer": result["messages"][-1].text}


if __name__ == "__main__":
    agent = Agent(
        "e2b",
        secret="e2b-secret",
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    agent.run(process, port=5000)
