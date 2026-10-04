import base64
import shlex
import uuid

from daytona import (
    AsyncDaytona,
    CreateSandboxFromSnapshotParams,
    CreateSecretParams,
    DaytonaNotFoundError,
    UpdateSecretParams,
)
from rebuno import CheckpointUnavailable, execution

WORKDIR = "/home/daytona/repo"
SECRET = "rebuno-github"


class DaytonaHandle:
    def __init__(self, sandbox):
        self.sandbox = sandbox
        self.paused = False

    @property
    def fs(self):
        return self.sandbox.fs

    @property
    def process(self):
        return self.sandbox.process

    async def pause(self):
        await self.sandbox.stop()
        self.paused = True


class DaytonaResource:
    driver_id = "example.daytona.v1"

    def __init__(self, repo, token):
        self.repo = repo
        self.token = token
        self.configuration = {"repo": repo}
        self.daytona = AsyncDaytona()

    async def _store_token(self):
        credentials = base64.b64encode(f"x-access-token:{self.token}".encode()).decode()
        found = await self.daytona.secret.list(name=SECRET)
        secret = next((s for s in found.items if s.name == SECRET), None)
        if secret:
            await self.daytona.secret.update(
                secret.id, UpdateSecretParams(value=credentials, hosts=["github.com"])
            )
        else:
            await self.daytona.secret.create(
                CreateSecretParams(name=SECRET, value=credentials, hosts=["github.com"])
            )

    async def _configure_git(self, sandbox):
        await sandbox.process.exec(
            "git config --global http.https://github.com/.extraheader"
            ' "Authorization: Basic $GITHUB_AUTH"'
        )

    async def create(self, checkpoint_ref=None):
        await self._store_token()
        try:
            sandbox = await self.daytona.create(
                CreateSandboxFromSnapshotParams(
                    snapshot=checkpoint_ref, secrets={"GITHUB_AUTH": SECRET}
                ),
                timeout=300,
            )
        except DaytonaNotFoundError as error:
            if checkpoint_ref:
                raise CheckpointUnavailable(
                    f"checkpoint missing: {checkpoint_ref}"
                ) from error
            raise
        await self._configure_git(sandbox)
        if checkpoint_ref is None:
            await sandbox.process.exec(
                f"git clone -q {shlex.quote(f'https://github.com/{self.repo}.git')} {WORKDIR}",
                timeout=120,
            )
        branch = shlex.quote(f"rebuno/{execution().id}")
        await sandbox.process.exec(
            f"git branch -m {branch}"
            " && (git branch --unset-upstream 2>/dev/null || true)"
            " && git config push.default current"
            " && git config user.name agent && git config user.email agent@example.com",
            cwd=WORKDIR,
        )
        return DaytonaHandle(sandbox), {"sandbox_id": sandbox.id}

    async def open(self, binding):
        await self._store_token()
        sandbox = await self.daytona.get(binding["sandbox_id"])
        if sandbox.state != "started":
            await sandbox.start(timeout=300)
        await self._configure_git(sandbox)
        return DaytonaHandle(sandbox)

    async def checkpoint(self, handle):
        if handle.paused:
            await handle.sandbox.start(timeout=300)
        name = f"rebuno-{uuid.uuid4().hex[:12]}"
        try:
            await handle.sandbox.create_snapshot(name, timeout=600)
            return name
        finally:
            if handle.paused:
                await handle.sandbox.stop()
