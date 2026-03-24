from __future__ import annotations

import abc
import json
import logging
import signal
import time
import uuid
from typing import Any

from rebuno._internal import jittered_backoff
from rebuno.client import RebunoClient
from rebuno.models import Job

logger = logging.getLogger("rebuno.runner")


class BaseRunner(abc.ABC):
    """Base class for synchronous Rebuno tool runners.

    Subclass and implement the ``execute`` method to define how tools are
    executed. Call ``run()`` to start the runner's SSE event loop.

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
        self._client = RebunoClient(
            base_url=kernel_url,
            api_key=api_key,
        )
        self._running = False

    @abc.abstractmethod
    def execute(self, tool_id: str, arguments: Any) -> Any:
        """Execute a tool with the given arguments.

        Args:
            tool_id: The tool to execute.
            arguments: Tool arguments from the step.

        Returns:
            Result data from the tool execution.
        """
        ...

    def run(self) -> None:
        """Start the runner event loop, connecting to the kernel SSE stream.

        Blocks until a shutdown signal is received or the runner is stopped.
        Automatically reconnects on connection failures with exponential backoff.
        """
        self._running = True

        def handle_signal(signum: int, frame: Any) -> None:
            logger.info("Shutdown signal received (signal=%d)", signum)
            self._running = False

        signal.signal(signal.SIGTERM, handle_signal)
        signal.signal(signal.SIGINT, handle_signal)

        logger.info(
            "Runner starting: runner_id=%s capabilities=%s",
            self.runner_id,
            self.capabilities,
        )

        consecutive_failures = 0
        while self._running:
            try:
                self._connect_and_process()
                consecutive_failures = 0
            except KeyboardInterrupt:
                break
            except Exception:
                consecutive_failures += 1
                logger.exception("SSE connection error")
                if self._running:
                    delay = jittered_backoff(
                        self.reconnect_delay, consecutive_failures, self.max_reconnect_delay,
                    )
                    time.sleep(delay)

        self._client.close()
        logger.info("Runner stopped")

    def _connect_and_process(self) -> None:
        for event in self._client.runner_stream(
            runner_id=self.runner_id,
            consumer_id=self.consumer_id,
            capabilities=self.capabilities,
        ):
            if not self._running:
                return
            if event.type == "job.assigned":
                job = Job(**json.loads(event.data))
                self._handle_job(job)

    def _handle_job(self, job: Job) -> None:
        logger.info(
            "Received job: job_id=%s tool_id=%s step_id=%s",
            job.id,
            job.tool_id,
            job.step_id,
        )

        try:
            self._client.step_started(
                step_id=job.step_id,
                execution_id=job.execution_id,
                runner_id=self.runner_id,
            )
        except Exception:
            logger.debug("Failed to report step started", exc_info=True)

        try:
            result = self.execute(job.tool_id, job.arguments)
        except Exception as e:
            try:
                self._client.submit_result(
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
            self._client.submit_result(
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
