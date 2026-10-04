import os

import httpx2
from langchain.agents import create_agent
from langchain_core.messages import messages_from_dict, messages_to_dict
from langchain_openai import ChatOpenAI
from rebuno import (
    Agent,
    Blocked,
    CheckpointPolicy,
    Result,
    execution,
    http_client,
    previous,
    resource,
    tool,
)
from vercel.sandbox import SandboxPathNotFoundError
from workspace_resource import WORKDIR, VercelResource

REPO = os.environ["REPO"]


async def process(task: str) -> Result:
    prior = await previous() or {}
    token = os.environ["GITHUB_TOKEN"]
    workspace = await resource(
        "workspace",
        driver=VercelResource(REPO, token),
        # Optional. Without it, the session still reopens the same sandbox;
        # checkpoints are only needed to fork it.
        checkpoints=CheckpointPolicy(every_steps=5),
    )

    @tool("shell", resources=["workspace"])
    async def shell(command: str) -> str:
        """Run a shell command in the repository and return its exit code and output."""
        result = await workspace.run_process(
            "bash", ["-c", command], cwd=WORKDIR, kill_after=120, capture_output=True
        )
        return f"exit code {result.returncode}\n{result.stdout}{result.stderr}"[-10000:]

    @tool("read_file")
    async def read_file(path: str) -> str:
        """Return a file's contents. The path is relative to the repository root."""
        try:
            return await workspace.fs.read_text(f"{WORKDIR}/{path}")
        except SandboxPathNotFoundError:
            return f"{path} does not exist"

    @tool("write_file", resources=["workspace"])
    async def write_file(path: str, content: str) -> str:
        """Replace a file's contents, creating it if needed. The path is relative to the repository root."""
        await workspace.fs.write_text(f"{WORKDIR}/{path}", content)
        return f"wrote {path}"

    @tool("open_pr", idempotency="at_most_once")
    async def open_pr(title: str, body: str) -> str:
        """Open a pull request from the pushed branch."""
        result = await workspace.run_process(
            "git", ["branch", "--show-current"], cwd=WORKDIR, capture_output=True
        )
        branch = result.stdout.strip()
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
You are working in a checkout of {REPO}. Use shell to run
commands in the repository, read_file to read a file, and write_file to replace
a file's contents. Run the tests after a change. When they pass, commit, push
the current branch with `git push -u origin HEAD`, and call open_pr once with a title
and a short description. Later pushes to the branch update the same pull
request.""",
    )

    result = None
    try:
        result = await graph.ainvoke(
            {
                "messages": [
                    *messages_from_dict(prior.get("messages", [])),
                    {"role": "user", "content": task},
                ]
            }
        )
    finally:
        if result is not None or isinstance(execution().suspension, Blocked):
            await workspace.pause()

    return Result(
        output={"answer": result["messages"][-1].text},
        state={
            "messages": messages_to_dict(result["messages"]),
        },
    )


if __name__ == "__main__":
    agent = Agent(
        "vercel",
        secret="vercel-secret",
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    agent.run(process, port=5000)
