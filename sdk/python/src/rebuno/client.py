from __future__ import annotations

import json
import logging
import time
from collections.abc import Iterator
from typing import Any

import httpx

from rebuno._version import __version__
from rebuno._internal import SSEEvent, api_error, parse_sse
from rebuno.errors import APIError, NetworkError, PolicyError, RebunoError
from rebuno.models import (
    Event,
    EventList,
    Execution,
    ExecutionStatus,
    IntentResult,
    ListExecutionsResult,
    SignalResult,
)

logger = logging.getLogger("rebuno")

USER_AGENT = f"rebuno-python/{__version__}"


class RebunoClient:
    """Synchronous client for the Rebuno kernel API.

    Provides methods for managing executions, sending signals, streaming
    events, and submitting tool results. Supports automatic retries with
    exponential backoff.

    Args:
        base_url: Base URL of the Rebuno kernel.
        api_key: Optional API key for authentication.
        timeout: Default request timeout in seconds.
        max_retries: Maximum number of retry attempts.
        retry_base_delay: Base delay for exponential backoff.
        retry_max_delay: Maximum delay between retries.
    """

    def __init__(
        self,
        base_url: str,
        api_key: str = "",
        timeout: float = 35.0,
        max_retries: int = 3,
        retry_base_delay: float = 1.0,
        retry_max_delay: float = 10.0,
    ):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.max_retries = max_retries
        self.retry_base_delay = retry_base_delay
        self.retry_max_delay = retry_max_delay

        headers: dict[str, str] = {
            "Content-Type": "application/json",
            "User-Agent": USER_AGENT,
        }
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        self._client = httpx.Client(
            base_url=self.base_url,
            headers=headers,
            timeout=timeout,
        )

    def close(self) -> None:
        """Close the underlying HTTP client and release resources."""
        self._client.close()

    def __enter__(self) -> RebunoClient:
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def _retry_delay(self, attempt: int) -> float:
        return min(self.retry_base_delay * (2**attempt), self.retry_max_delay)

    def _request(
        self,
        method: str,
        path: str,
        json: dict[str, Any] | None = None,
        params: dict[str, Any] | None = None,
        idempotent: bool = False,
    ) -> httpx.Response:
        retryable = method == "GET" or idempotent
        last_error: Exception | None = None
        for attempt in range(self.max_retries + 1):
            try:
                resp = self._client.request(method, path, json=json, params=params)
                if resp.status_code == 429:
                    if attempt >= self.max_retries:
                        raise api_error(resp)
                    retry_after_str = resp.headers.get("Retry-After", "1")
                    try:
                        retry_after = float(retry_after_str)
                    except ValueError:
                        retry_after = 1.0
                    time.sleep(retry_after)
                    continue
                if resp.status_code == 403:
                    body = resp.json() if resp.content else {}
                    raise PolicyError(
                        body.get("error", "Forbidden"),
                        rule_id=body.get("rule_id", ""),
                    )
                if resp.status_code >= 500 and retryable:
                    last_error = api_error(resp)
                    if attempt >= self.max_retries:
                        break
                    delay = self._retry_delay(attempt)
                    logger.warning(
                        "Server error %d, retrying in %.1fs (attempt %d/%d)",
                        resp.status_code,
                        delay,
                        attempt + 1,
                        self.max_retries + 1,
                    )
                    time.sleep(delay)
                    continue
                if resp.status_code >= 400:
                    raise api_error(resp)
                return resp
            except (httpx.ConnectError, httpx.TimeoutException) as e:
                last_error = NetworkError(str(e))
                if not retryable:
                    raise last_error
                if attempt >= self.max_retries:
                    break
                delay = self._retry_delay(attempt)
                logger.warning(
                    "Connection error, retrying in %.1fs (attempt %d/%d)",
                    delay,
                    attempt + 1,
                    self.max_retries + 1,
                )
                time.sleep(delay)
            except (APIError, PolicyError):
                raise
            except Exception as e:
                raise RebunoError(str(e))
        raise last_error or RebunoError("max retries exceeded")

    def create_execution(
        self,
        agent_id: str,
        input: Any = None,
        labels: dict[str, str] | None = None,
    ) -> Execution:
        """Create a new execution for the given agent.

        Args:
            agent_id: ID of the agent to create the execution for.
            input: Optional input data for the execution.
            labels: Optional key-value labels.

        Returns:
            Execution with full execution state.
        """
        body: dict[str, Any] = {"agent_id": agent_id}
        if input is not None:
            body["input"] = input
        if labels:
            body["labels"] = labels
        resp = self._request("POST", "/v0/executions", json=body)
        return Execution(**resp.json())

    def get_execution(self, execution_id: str) -> Execution:
        """Retrieve the full details of an execution by ID.

        Args:
            execution_id: The execution to retrieve.

        Returns:
            Execution model with current state.
        """
        resp = self._request("GET", f"/v0/executions/{execution_id}")
        return Execution(**resp.json())

    def list_executions(
        self,
        status: ExecutionStatus | None = None,
        agent_id: str = "",
        limit: int = 50,
        cursor: str = "",
    ) -> ListExecutionsResult:
        """List executions with optional filtering and pagination.

        Args:
            status: Filter by execution status.
            agent_id: Filter by agent ID.
            limit: Maximum number of results to return.
            cursor: Pagination cursor from a previous response.

        Returns:
            ListExecutionsResult with executions and next_cursor.
        """
        params: dict[str, Any] = {"limit": limit}
        if status:
            params["status"] = status.value
        if agent_id:
            params["agent_id"] = agent_id
        if cursor:
            params["cursor"] = cursor
        resp = self._request("GET", "/v0/executions", params=params)
        return ListExecutionsResult(**resp.json())

    def cancel_execution(self, execution_id: str) -> Execution:
        """Cancel a running execution.

        Args:
            execution_id: The execution to cancel.

        Returns:
            Updated Execution model.
        """
        resp = self._request(
            "POST", f"/v0/executions/{execution_id}/cancel", idempotent=True
        )
        return Execution(**resp.json())

    def send_signal(
        self, execution_id: str, signal_type: str, payload: Any = None
    ) -> SignalResult:
        """Send a signal to a running execution.

        Args:
            execution_id: Target execution ID.
            signal_type: Type identifier for the signal.
            payload: Optional payload data.

        Returns:
            SignalResult with status.
        """
        body: dict[str, Any] = {"signal_type": signal_type}
        if payload is not None:
            body["payload"] = payload
        resp = self._request(
            "POST", f"/v0/executions/{execution_id}/signal", json=body
        )
        return SignalResult(**resp.json())

    def get_events(
        self, execution_id: str, after_sequence: int = 0, limit: int = 100
    ) -> EventList:
        """Retrieve events for an execution.

        Args:
            execution_id: The execution to get events for.
            after_sequence: Only return events after this sequence number.
            limit: Maximum number of events to return.

        Returns:
            EventList with events and latest_sequence.
        """
        params = {"after_sequence": after_sequence, "limit": limit}
        resp = self._request(
            "GET", f"/v0/executions/{execution_id}/events", params=params
        )
        return EventList(**resp.json())

    def stream_events(
        self, execution_id: str, after_sequence: int = 0
    ) -> Iterator[Event]:
        """Stream execution events in real-time via SSE.

        Replays historical events from after_sequence, then streams new ones
        as they occur. The iterator ends when a terminal event is received.

        Args:
            execution_id: The execution to stream events for.
            after_sequence: Only return events after this sequence number.

        Yields:
            Event instances as they arrive.
        """
        params: dict[str, Any] = {}
        if after_sequence:
            params["after_sequence"] = after_sequence
        with self._client.stream(
            "GET",
            f"/v0/executions/{execution_id}/stream",
            params=params,
            headers={"Accept": "text/event-stream"},
            timeout=None,
        ) as response:
            response.raise_for_status()
            for sse in parse_sse(response.iter_lines()):
                yield Event(**json.loads(sse.data))

    def stream(
        self, agent_id: str, consumer_id: str
    ) -> Iterator[SSEEvent]:
        """Open an SSE stream for agent execution assignments.

        Args:
            agent_id: The agent to receive assignments for.
            consumer_id: Unique consumer identifier.

        Yields:
            SSEEvent instances from the stream.
        """
        params = {"agent_id": agent_id, "consumer_id": consumer_id}
        with self._client.stream(
            "GET",
            "/v0/agents/stream",
            params=params,
            headers={"Accept": "text/event-stream"},
            timeout=None,
        ) as response:
            response.raise_for_status()
            yield from parse_sse(response.iter_lines())

    def submit_intent(
        self,
        execution_id: str,
        session_id: str,
        intent_type: str,
        tool_id: str = "",
        arguments: Any = None,
        idempotency_key: str = "",
        signal_type: str = "",
        output: Any = None,
        error: str = "",
        remote: bool = False,
    ) -> IntentResult:
        """Submit an intent (tool invocation, wait, complete, fail) for an execution.

        Args:
            execution_id: The target execution.
            session_id: The agent session ID.
            intent_type: One of 'invoke_tool', 'wait', 'complete', 'fail'.
            tool_id: Tool to invoke (for invoke_tool intents).
            arguments: Tool arguments.
            idempotency_key: Key for deduplication.
            signal_type: Signal type (for wait intents).
            output: Output data (for complete intents).
            error: Error message (for fail intents).
            remote: Whether the tool should be dispatched remotely.

        Returns:
            IntentResult indicating acceptance and step_id.
        """
        intent: dict[str, Any] = {"type": intent_type}
        if tool_id:
            intent["tool_id"] = tool_id
        if arguments is not None:
            intent["arguments"] = arguments
        if idempotency_key:
            intent["idempotency_key"] = idempotency_key
        if signal_type:
            intent["signal_type"] = signal_type
        if output is not None:
            intent["output"] = output
        if error:
            intent["error"] = error
        if remote:
            intent["remote"] = True

        body = {
            "execution_id": execution_id,
            "session_id": session_id,
            "intent": intent,
        }
        resp = self._request("POST", "/v0/agents/intent", json=body, idempotent=True)
        return IntentResult(**resp.json())

    def report_step_result(
        self,
        execution_id: str,
        session_id: str,
        step_id: str,
        success: bool,
        data: Any = None,
        error: str = "",
    ) -> None:
        """Report the result of a locally executed step.

        Args:
            execution_id: The execution owning the step.
            session_id: The agent session ID.
            step_id: The step that completed.
            success: Whether the step succeeded.
            data: Result data on success.
            error: Error message on failure.
        """
        body: dict[str, Any] = {
            "execution_id": execution_id,
            "session_id": session_id,
            "step_id": step_id,
            "success": success,
        }
        if data is not None:
            body["data"] = data
        if error:
            body["error"] = error
        self._request("POST", "/v0/agents/step-result", json=body, idempotent=True)

    def submit_result(
        self,
        runner_id: str,
        job_id: str,
        execution_id: str,
        step_id: str,
        success: bool,
        data: Any = None,
        error: str = "",
        retryable: bool = False,
        started_at: str | None = None,
        completed_at: str | None = None,
    ) -> dict[str, Any]:
        """Submit the result of a runner job.

        Args:
            runner_id: ID of the runner submitting the result.
            job_id: The job that was executed.
            execution_id: The owning execution.
            step_id: The step that was executed.
            success: Whether the job succeeded.
            data: Result data on success.
            error: Error message on failure.
            retryable: Whether the failure is retryable.
            started_at: Optional ISO timestamp of when execution started.
            completed_at: Optional ISO timestamp of when execution completed.

        Returns:
            Raw response dict from the API.
        """
        body: dict[str, Any] = {
            "job_id": job_id,
            "execution_id": execution_id,
            "step_id": step_id,
            "success": success,
        }
        if data is not None:
            body["data"] = data
        if error:
            body["error"] = error
        if retryable:
            body["retryable"] = retryable
        if started_at is not None:
            body["started_at"] = started_at
        if completed_at is not None:
            body["completed_at"] = completed_at
        resp = self._request(
            "POST", f"/v0/runners/{runner_id}/results", json=body, idempotent=True
        )
        return resp.json()

    def step_started(
        self, step_id: str, execution_id: str, runner_id: str
    ) -> None:
        """Notify the kernel that a runner has started executing a step.

        Args:
            step_id: The step being started.
            execution_id: The owning execution.
            runner_id: The runner executing the step.
        """
        self._request(
            "POST",
            f"/v0/runners/steps/{step_id}/started",
            json={"execution_id": execution_id, "runner_id": runner_id},
        )

    def runner_stream(
        self, runner_id: str, consumer_id: str, capabilities: list[str] | None = None,
    ) -> Iterator[SSEEvent]:
        """Open an SSE stream for runner job assignments.

        Args:
            runner_id: The runner to receive jobs for.
            consumer_id: Unique consumer identifier.
            capabilities: Optional list of tool capabilities.

        Yields:
            SSEEvent instances from the stream.
        """
        params: dict[str, str] = {
            "runner_id": runner_id,
            "consumer_id": consumer_id,
        }
        if capabilities:
            params["capabilities"] = ",".join(capabilities)

        with self._client.stream(
            "GET",
            "/v0/runners/stream",
            params=params,
            headers={"Accept": "text/event-stream"},
            timeout=None,
        ) as response:
            response.raise_for_status()
            yield from parse_sse(response.iter_lines())

    def update_capabilities(
        self, runner_id: str, tools: list[str]
    ) -> None:
        """Update the capability list for a connected runner.

        Args:
            runner_id: The runner to update.
            tools: New list of tool IDs this runner supports.
        """
        self._request(
            "POST",
            f"/v0/runners/{runner_id}/capabilities",
            json={"tools": tools},
        )

    def unregister_runner(self, runner_id: str) -> None:
        """Unregister a runner from the kernel.

        Args:
            runner_id: The runner to unregister.
        """
        self._request("DELETE", f"/v0/runners/{runner_id}")

    def health(self) -> dict[str, str]:
        """Check the health status of the kernel.

        Returns:
            Health status dict.
        """
        resp = self._request("GET", "/v0/health")
        return resp.json()

    def ready(self) -> dict[str, str]:
        """Check the readiness status of the kernel.

        Returns:
            Readiness status dict.
        """
        resp = self._request("GET", "/v0/ready")
        return resp.json()
