from __future__ import annotations

import asyncio
import json
import logging
import signal
import uuid
from typing import Any

from rebuno._internal import async_parse_sse, jittered_backoff
from rebuno.async_client import AsyncRebunoClient
from rebuno.mcp import McpManager
from rebuno.models import Job

logger = logging.getLogger("rebuno.runner")


class AsyncBaseRunner:
    """Base class for asynchronous Rebuno tool runners.

    Subclass and implement the ``execute`` method to define how tools are
    executed. Call ``await run()`` to start the runner's SSE event loop.

    Args:
        runner_id: Unique runner identifier.
        kernel_url: Base URL of the Rebuno kernel.
        capabilities: List of tool capabilities this runner supports.
        api_key: Optional API key for authentication.
        name: Display name for the runner.
        reconnect_delay: Initial reconnect delay in seconds.
        max_reconnect_delay: Maximum reconnect delay in seconds.
    """

    def __init__(
        self,
        runner_id: str,
        kernel_url: str,
        capabilities: list[str] | None = None,
        api_key: str = "",
        name: str = "",
        reconnect_delay: float = 2.0,
        max_reconnect_delay: float = 60.0,
    ):
        if not runner_id:
            raise ValueError("runner_id must not be empty")
        if not kernel_url:
            raise ValueError("kernel_url must not be empty")
        self.runner_id = runner_id
        self.name = name or runner_id
        self.capabilities = capabilities or []
        self.consumer_id = f"{runner_id}-{uuid.uuid4().hex[:8]}"
        self.reconnect_delay = reconnect_delay
        self.max_reconnect_delay = max_reconnect_delay
        self._api_key = api_key
        self._client = AsyncRebunoClient(
            base_url=kernel_url,
            api_key=api_key,
        )
        self._running = False
        self._shutdown_event = asyncio.Event()
        self._connect_task: asyncio.Task[None] | None = None
        self._mcp: McpManager | None = None

    async def execute(self, tool_id: str, arguments: Any) -> Any:
        """Execute a tool with the given arguments.

        Override this method to handle non-MCP tools. If only MCP servers
        are configured, this method does not need to be overridden.

        Args:
            tool_id: The tool to execute.
            arguments: Tool arguments from the step.

        Returns:
            Result data from the tool execution.
        """
        raise NotImplementedError(
            f"No handler for tool '{tool_id}'. Override execute() or add an MCP server."
        )

    def _ensure_mcp(self) -> McpManager:
        if self._mcp is None:
            self._mcp = McpManager()
        return self._mcp

    def mcp_server(
        self,
        name: str,
        *,
        command: str = "",
        args: list[str] | None = None,
        env: dict[str, str] | None = None,
        url: str = "",
        headers: dict[str, str] | None = None,
        prefix: str = "",
    ) -> None:
        """Register an MCP server whose tools will be available for remote execution.

        Args:
            name: Display name for the server.
            command: Command to start a stdio MCP server.
            args: Arguments for the command.
            env: Environment variables for the subprocess.
            url: URL for an HTTP MCP server.
            headers: HTTP headers for the connection.
            prefix: Tool ID prefix (defaults to name).
        """
        self._ensure_mcp().add_server(
            name, command=command, args=args, env=env,
            url=url, headers=headers, prefix=prefix,
        )

    def mcp_servers_from_config(self, config: dict[str, Any]) -> None:
        """Register MCP servers from a standard mcpServers config dict.

        Args:
            config: Dict with "mcpServers" key or direct server mapping.
        """
        self._ensure_mcp().add_servers_from_config(config)

    async def run(self) -> None:
        """Start the async runner event loop, connecting to the kernel SSE stream.

        Blocks until a shutdown signal is received or the runner is stopped.
        Automatically reconnects on connection failures with exponential backoff.
        """
        self._running = True
        self._shutdown_event.clear()

        loop = asyncio.get_running_loop()
        try:
            loop.add_signal_handler(signal.SIGTERM, self._handle_shutdown)
            loop.add_signal_handler(signal.SIGINT, self._handle_shutdown)
        except NotImplementedError:
            signal.signal(signal.SIGTERM, lambda s, f: loop.call_soon_threadsafe(self._handle_shutdown))
            signal.signal(signal.SIGINT, lambda s, f: loop.call_soon_threadsafe(self._handle_shutdown))

        logger.info(
            "Runner starting: runner_id=%s capabilities=%s",
            self.runner_id,
            self.capabilities,
        )

        consecutive_failures = 0
        try:
            while self._running:
                try:
                    self._connect_task = asyncio.ensure_future(
                        self._connect_and_process()
                    )
                    await self._connect_task
                    consecutive_failures = 0
                except asyncio.CancelledError:
                    break
                except Exception:
                    consecutive_failures += 1
                    logger.exception("SSE connection error")
                    if self._running:
                        delay = jittered_backoff(
                            self.reconnect_delay, consecutive_failures, self.max_reconnect_delay,
                        )
                        try:
                            await asyncio.wait_for(
                                self._shutdown_event.wait(),
                                timeout=delay,
                            )
                            break
                        except asyncio.TimeoutError:
                            pass
        finally:
            if self._mcp is not None:
                await self._mcp.disconnect_all()
            await self._client.close()
            logger.info("Runner stopped")

    def _handle_shutdown(self) -> None:
        logger.info("Shutdown signal received")
        self._running = False
        self._shutdown_event.set()
        if self._connect_task and not self._connect_task.done():
            self._connect_task.cancel()

    async def _merged_capabilities(self) -> list[str]:
        if self._mcp is None:
            return self.capabilities
        if not any(c.connected for c in self._mcp._connections.values()):
            await self._mcp.connect_all()
        mcp_tool_ids = await self._mcp.all_tool_ids()
        return list(set(self.capabilities + mcp_tool_ids))

    async def _connect_and_process(self) -> None:
        all_caps = await self._merged_capabilities()

        params: dict[str, str] = {
            "runner_id": self.runner_id,
            "consumer_id": self.consumer_id,
        }
        if all_caps:
            params["capabilities"] = ",".join(all_caps)

        headers: dict[str, str] = {"Accept": "text/event-stream"}
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"

        async with self._client._client.stream(
            "GET",
            "/v0/runners/stream",
            params=params,
            headers=headers,
            timeout=None,
        ) as response:
            response.raise_for_status()

            if self._mcp is not None and self._mcp._failed:
                async def _on_reconnect() -> None:
                    new_caps = await self._merged_capabilities()
                    await self._client.update_capabilities(self.runner_id, new_caps)

                self._mcp.start_retry_loop(on_reconnect=_on_reconnect)

            async for event in async_parse_sse(response.aiter_lines()):
                if not self._running:
                    return
                if event.type == "job.assigned":
                    job = Job(**json.loads(event.data))
                    await self._handle_job(job)

    async def _handle_job(self, job: Job) -> None:
        logger.info(
            "Received job: job_id=%s tool_id=%s step_id=%s",
            job.id,
            job.tool_id,
            job.step_id,
        )

        try:
            await self._client.step_started(
                step_id=job.step_id,
                execution_id=job.execution_id,
                runner_id=self.runner_id,
            )
        except Exception:
            logger.debug("Failed to report step started", exc_info=True)

        try:
            if self._mcp is not None and self._mcp.has_tool(job.tool_id):
                result = await self._mcp.call_tool(job.tool_id, job.arguments)
            else:
                result = await self.execute(job.tool_id, job.arguments)
        except Exception as e:
            try:
                await self._client.submit_result(
                    runner_id=self.runner_id,
                    job_id=job.id,
                    execution_id=job.execution_id,
                    step_id=job.step_id,
                    success=False,
                    error=str(e),
                    retryable=getattr(e, "retryable", False),
                )
            except Exception:
                logger.exception("Failed to submit failure result: job_id=%s", job.id)
            logger.warning("Job failed: job_id=%s error=%s", job.id, str(e))
            return

        try:
            await self._client.submit_result(
                runner_id=self.runner_id,
                job_id=job.id,
                execution_id=job.execution_id,
                step_id=job.step_id,
                success=True,
                data=result,
            )
            logger.info("Job completed: job_id=%s", job.id)
        except Exception:
            logger.exception("Failed to submit success result: job_id=%s", job.id)
