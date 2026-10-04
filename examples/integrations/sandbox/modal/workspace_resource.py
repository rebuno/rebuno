import base64
import hashlib
import shlex

import modal
import modal.experimental
from modal.exception import NotFoundError
from rebuno import CheckpointUnavailable, execution

WORKDIR = "/root/repo"
IMAGE = modal.Image.debian_slim().apt_install("git")


class ModalResource:
    driver_id = "example.modal.v1"

    def __init__(self, repo, token):
        self.repo = repo
        self.token = token
        self.configuration = {"repo": repo}

    async def _outbound_policy(self):
        credentials = base64.b64encode(f"x-access-token:{self.token}".encode()).decode()
        name = f"rebuno-github-{hashlib.sha256(credentials.encode()).hexdigest()[:16]}"
        await modal.Secret.objects.create.aio(
            name, {"GITHUB_BASIC": credentials}, allow_existing=True
        )
        return modal.experimental.OutboundPolicy().with_header_replacement(
            domain="github.com",
            secret=modal.Secret.from_name(name),
            headers={"Authorization": "Basic $GITHUB_BASIC"},
        )

    async def _run(self, sandbox, command, **kwargs):
        process = await sandbox.exec.aio("bash", "-c", command, **kwargs)
        await process.wait.aio()

    async def create(self, checkpoint_ref=None):
        app = await modal.App.lookup.aio("rebuno", create_if_missing=True)
        try:
            sandbox = await modal.Sandbox.create.aio(
                app=app,
                image=modal.Image.from_id(checkpoint_ref) if checkpoint_ref else IMAGE,
                timeout=3600,
                _experimental_outbound_policy=await self._outbound_policy(),
            )
        except NotFoundError as error:
            if checkpoint_ref:
                raise CheckpointUnavailable(
                    f"checkpoint missing: {checkpoint_ref}"
                ) from error
            raise
        if checkpoint_ref is None:
            await self._run(
                sandbox,
                f"git clone -q {shlex.quote(f'https://github.com/{self.repo}.git')} {WORKDIR}",
                timeout=120,
            )
        branch = shlex.quote(f"rebuno/{execution().id}")
        await self._run(
            sandbox,
            f"git branch -m {branch}"
            " && (git branch --unset-upstream 2>/dev/null || true)"
            " && git config push.default current"
            " && git config user.name agent && git config user.email agent@example.com",
            workdir=WORKDIR,
        )
        return sandbox, {"sandbox_id": sandbox.object_id}

    async def open(self, binding):
        sandbox = await modal.Sandbox.from_id.aio(binding["sandbox_id"])
        await sandbox._experimental_update_outbound_policy.aio(
            await self._outbound_policy()
        )
        return sandbox

    async def checkpoint(self, handle):
        image = await handle.snapshot_filesystem.aio()
        return image.object_id
