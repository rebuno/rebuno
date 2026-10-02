import base64
import shlex

from e2b import AsyncSandbox, SandboxException
from rebuno import CheckpointUnavailable, execution

WORKDIR = "/home/user/repo"


class E2BHandle:
    def __init__(self, sandbox):
        self.sandbox = sandbox
        self.paused = False

    @property
    def files(self):
        return self.sandbox.files

    @property
    def commands(self):
        return self.sandbox.commands

    async def pause(self):
        await self.sandbox.pause()
        self.paused = True


class E2BResource:
    driver_id = "example.e2b.v1"

    def __init__(self, repo, token):
        self.repo = repo
        self.token = token
        self.configuration = {"repo": repo}

    async def _configure_network(self, sandbox):
        credentials = base64.b64encode(f"x-access-token:{self.token}".encode()).decode()
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

    async def create(self, checkpoint_ref=None):
        try:
            sandbox = await AsyncSandbox.create(
                checkpoint_ref, timeout=600, lifecycle={"on_timeout": "pause"}
            )
        except SandboxException as error:
            if checkpoint_ref and error.status_code == 404:
                raise CheckpointUnavailable(
                    f"checkpoint missing: {checkpoint_ref}"
                ) from error
            raise
        await self._configure_network(sandbox)
        if checkpoint_ref is None:
            await sandbox.commands.run(
                f"git clone -q {shlex.quote(f'https://github.com/{self.repo}.git')} {WORKDIR}",
                timeout=120,
            )
        branch = shlex.quote(f"rebuno/{execution().id}")
        await sandbox.commands.run(
            f"git branch -m {branch}"
            " && (git branch --unset-upstream 2>/dev/null || true)"
            " && git config push.default current"
            " && git config user.name agent && git config user.email agent@example.com",
            cwd=WORKDIR,
        )
        return E2BHandle(sandbox), {"sandbox_id": sandbox.sandbox_id}

    async def open(self, binding):
        sandbox = await AsyncSandbox.connect(binding["sandbox_id"], timeout=600)
        await self._configure_network(sandbox)
        return E2BHandle(sandbox)

    async def checkpoint(self, handle):
        if handle.paused:
            handle.sandbox = await AsyncSandbox.connect(
                handle.sandbox.sandbox_id, timeout=600
            )
        try:
            snapshot = await handle.sandbox.create_snapshot()
            handle.sandbox = await AsyncSandbox.connect(
                handle.sandbox.sandbox_id, timeout=600
            )
            return snapshot.snapshot_id
        finally:
            if handle.paused:
                await handle.sandbox.pause()
