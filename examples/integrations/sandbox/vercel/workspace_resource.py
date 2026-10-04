import base64
import shlex

from rebuno import CheckpointUnavailable, execution
from vercel import sandbox as vercel
from vercel.sandbox import (
    NetworkPolicy,
    NetworkPolicyRule,
    NetworkPolicyTransform,
    SandboxApiError,
    SnapshotSource,
)

WORKDIR = "/vercel/sandbox/repo"


class VercelHandle:
    def __init__(self, sandbox):
        self.sandbox = sandbox
        self.paused = False

    @property
    def fs(self):
        return self.sandbox.fs

    @property
    def run_process(self):
        return self.sandbox.run_process

    async def pause(self):
        await self.sandbox.stop()
        self.paused = True


class VercelResource:
    driver_id = "example.vercel.v1"

    def __init__(self, repo, token):
        self.repo = repo
        self.token = token
        self.configuration = {"repo": repo}

    def _network_policy(self):
        credentials = base64.b64encode(f"x-access-token:{self.token}".encode()).decode()
        return NetworkPolicy.custom(
            {
                "*": [],
                "github.com": [
                    NetworkPolicyRule(
                        transform=[
                            NetworkPolicyTransform(
                                headers={"Authorization": f"Basic {credentials}"}
                            )
                        ]
                    )
                ],
            }
        )

    async def create(self, checkpoint_ref=None):
        try:
            sandbox = await vercel.create_sandbox(
                source=SnapshotSource(snapshot_id=checkpoint_ref)
                if checkpoint_ref
                else None,
                execution_time_limit=600,
                network_policy=self._network_policy(),
            )
        except SandboxApiError as error:
            if checkpoint_ref and error.status_code == 404:
                raise CheckpointUnavailable(
                    f"checkpoint missing: {checkpoint_ref}"
                ) from error
            raise
        if checkpoint_ref is None:
            await sandbox.run_process(
                "git",
                ["clone", "-q", f"https://github.com/{self.repo}.git", WORKDIR],
                kill_after=120,
            )
        branch = shlex.quote(f"rebuno/{execution().id}")
        await sandbox.run_process(
            "bash",
            [
                "-c",
                (
                    f"git branch -m {branch}"
                    " && (git branch --unset-upstream 2>/dev/null || true)"
                    " && git config push.default current"
                    " && git config user.name agent && git config user.email agent@example.com"
                ),
            ],
            cwd=WORKDIR,
        )
        return VercelHandle(sandbox), {"name": sandbox.name}

    async def open(self, binding):
        sandbox = await vercel.resume_sandbox(name=binding["name"])
        await sandbox.update_network_policy(self._network_policy())
        return VercelHandle(sandbox)

    async def checkpoint(self, handle):
        if handle.paused:
            handle.sandbox = await vercel.resume_sandbox(name=handle.sandbox.name)
        try:
            snapshot = await handle.sandbox.snapshot()
            handle.sandbox = await vercel.resume_sandbox(name=handle.sandbox.name)
            return snapshot.id
        finally:
            if handle.paused:
                await handle.sandbox.stop()
