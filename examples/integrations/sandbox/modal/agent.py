import os

import httpx2
from langchain.agents import create_agent
from langchain_core.messages import messages_from_dict, messages_to_dict
from langchain_openai import ChatOpenAI
from modal.exception import SandboxFilesystemNotFoundError
from rebuno import (
    Agent,
    CheckpointPolicy,
    Result,
    http_client,
    previous,
    resource,
    tool,
)
from workspace_resource import WORKDIR, ModalResource

REPO = os.environ["REPO"]


async def process(task: str) -> Result:
    prior = await previous() or {}
    token = os.environ["GITHUB_TOKEN"]
    workspace = await resource(
        "workspace",
        driver=ModalResource(REPO, token),
        # Optional. Without it, the session still reopens the same sandbox;
        # checkpoints are only needed to fork it.
        checkpoints=CheckpointPolicy(every_steps=5),
    )

    @tool("shell", resources=["workspace"])
    async def shell(command: str) -> str:
        """Run a shell command in the repository and return its exit code and output."""
        process = await workspace.exec.aio(
            "bash", "-c", command, workdir=WORKDIR, timeout=120
        )
        stdout = await process.stdout.read.aio()
        stderr = await process.stderr.read.aio()
        exit_code = await process.wait.aio()
        return f"exit code {exit_code}\n{stdout}{stderr}"[-10000:]

    @tool("read_file")
    async def read_file(path: str) -> str:
        """Return a file's contents. The path is relative to the repository root."""
        try:
            return await workspace.filesystem.read_text.aio(f"{WORKDIR}/{path}")
        except SandboxFilesystemNotFoundError:
            return f"{path} does not exist"

    @tool("write_file", resources=["workspace"])
    async def write_file(path: str, content: str) -> str:
        """Replace a file's contents, creating it if needed. The path is relative to the repository root."""
        await workspace.filesystem.write_text.aio(content, f"{WORKDIR}/{path}")
        return f"wrote {path}"

    @tool("open_pr", idempotency="at_most_once")
    async def open_pr(title: str, body: str) -> str:
        """Open a pull request from the pushed branch."""
        process = await workspace.exec.aio(
            "git", "branch", "--show-current", workdir=WORKDIR
        )
        branch = (await process.stdout.read.aio()).strip()
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

    result = await graph.ainvoke(
        {
            "messages": [
                *messages_from_dict(prior.get("messages", [])),
                {"role": "user", "content": task},
            ]
        }
    )

    return Result(
        output={"answer": result["messages"][-1].text},
        state={
            "messages": messages_to_dict(result["messages"]),
        },
    )


if __name__ == "__main__":
    agent = Agent(
        "modal",
        secret="modal-secret",
        base_url=os.environ.get("REBUNO_URL", "http://localhost:8080"),
    )
    agent.run(process, port=5000)
