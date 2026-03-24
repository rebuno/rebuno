from __future__ import annotations

import abc
import functools
import inspect
import json
import logging
import signal
import threading
import time
import uuid
from collections.abc import Callable
from typing import Any

from rebuno._internal import SSEEvent, jittered_backoff, parse_sse
from rebuno.client import RebunoClient
from rebuno.errors import PolicyError, RebunoError, ToolError
from rebuno.models import ClaimResult, HistoryEntry

logger = logging.getLogger("rebuno.agent")


class AgentContext:
    """Context for a single synchronous agent execution.

    Provides methods to invoke tools, wait for signals, and manage step
    results. Each execution gets its own isolated event/data storage to
    prevent concurrent executions from interfering with each other.

    Attributes:
        execution_id: The ID of the current execution.
        session_id: The agent session ID.
        agent_id: The agent handling this execution.
        input: Input data provided when the execution was created.
        labels: Key-value labels from the execution.
        history: Previous step history for this execution.
    """

    def __init__(
        self,
        client: RebunoClient,
        claim: ClaimResult,
        tools: dict[str, Callable[..., Any]] | None = None,
        remote_tools: dict[str, Callable[..., Any]] | None = None,
        result_events: dict[str, threading.Event] | None = None,
        result_data: dict[str, dict[str, Any]] | None = None,
        signal_events: dict[str, threading.Event] | None = None,
        signal_data: dict[str, Any] | None = None,
        approval_events: dict[str, threading.Event] | None = None,
        approval_data: dict[str, dict[str, Any]] | None = None,
        wait_timeout: float = 3600.0,
    ):
        self._client = client
        self._claim = claim
        self._tools = tools or {}
        self._remote_tools = remote_tools or {}
        self._local_results: dict[str, Any] = {}
        self._result_events = result_events if result_events is not None else {}
        self._result_data = result_data if result_data is not None else {}
        self._signal_events = signal_events if signal_events is not None else {}
        self._signal_data = signal_data if signal_data is not None else {}
        self._approval_events = approval_events if approval_events is not None else {}
        self._approval_data = approval_data if approval_data is not None else {}
        self._wait_timeout = wait_timeout
        self.execution_id = claim.execution_id
        self.session_id = claim.session_id
        self.agent_id = claim.agent_id
        self.input = claim.input
        self.labels = claim.labels
        self.history: list[HistoryEntry] = claim.history

    def _is_local(self, tool_id: str) -> bool:
        return tool_id in self._tools

    def invoke_tool(
        self,
        tool_id: str,
        arguments: Any = None,
        idempotency_key: str = "",
    ) -> Any:
        """Invoke a tool and block until the result is available.

        Args:
            tool_id: The tool to invoke.
            arguments: Arguments to pass to the tool.
            idempotency_key: Optional key for deduplication.

        Returns:
            The tool's result data.

        Raises:
            PolicyError: If the intent is denied by policy.
            ToolError: If the tool execution fails.
        """
        if not idempotency_key:
            idempotency_key = f"{self.execution_id}:{tool_id}:{uuid.uuid4().hex[:8]}"

        local = self._is_local(tool_id)

        result = self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="invoke_tool",
            tool_id=tool_id,
            arguments=arguments,
            idempotency_key=idempotency_key,
            remote=not local,
        )

        if not result.accepted:
            raise PolicyError(result.error or "Intent denied by policy")

        step_id = result.step_id
        if not step_id:
            raise RebunoError("No step_id returned for invoke_tool intent")

        if result.pending_approval:
            approval = self._wait_for_approval(step_id)
            if not approval.get("approved", False):
                raise PolicyError("Tool invocation denied by human approval")

        if local:
            return self._execute_local(step_id, tool_id, arguments)
        return self._wait_for_result(step_id, tool_id)

    def get_tools(self) -> list[Callable]:
        """Return wrapped callables for all registered tools (local and remote).

        Each wrapper invokes the tool through the kernel intent flow.
        """
        all_tools = {**self._tools, **self._remote_tools}
        return [self._wrap_tool(tid, fn) for tid, fn in all_tools.items()]

    def _wrap_tool(self, tool_id: str, fn: Callable[..., Any]) -> Callable[..., Any]:
        ctx = self

        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            sig = inspect.signature(fn)
            bound = sig.bind(*args, **kwargs)
            bound.apply_defaults()
            return ctx.invoke_tool(tool_id, dict(bound.arguments))

        return wrapper

    def _execute_local(self, step_id: str, tool_id: str, arguments: Any) -> Any:
        fn = self._tools[tool_id]
        if arguments is not None and not isinstance(arguments, dict):
            logger.warning("Tool %s received non-dict arguments (%s), using {}", tool_id, type(arguments).__name__)
        kwargs = arguments if isinstance(arguments, dict) else {}

        try:
            output = fn(**kwargs)
        except Exception as e:
            self._client.report_step_result(
                execution_id=self.execution_id,
                session_id=self.session_id,
                step_id=step_id,
                success=False,
                error=str(e),
            )
            if isinstance(e, ToolError):
                raise
            raise ToolError(
                message=str(e),
                tool_id=tool_id,
                step_id=step_id,
            ) from e

        self._client.report_step_result(
            execution_id=self.execution_id,
            session_id=self.session_id,
            step_id=step_id,
            success=True,
            data=output,
        )
        return output

    def _wait_for_result(self, step_id: str, tool_id: str) -> Any:
        event = threading.Event()
        self._result_events[step_id] = event

        if step_id in self._result_data:
            event.set()

        if not event.wait(timeout=self._wait_timeout):
            raise RebunoError(f"Timed out waiting for tool result (step_id={step_id})")

        data = self._result_data.get(step_id, {})
        if data.get("status") == "failed":
            raise ToolError(
                message=data.get("error", "Tool execution failed"),
                tool_id=tool_id,
                step_id=step_id,
            )
        return data.get("result")

    def _wait_for_approval(self, step_id: str) -> dict[str, Any]:
        event = threading.Event()
        self._approval_events[step_id] = event

        if step_id in self._approval_data:
            event.set()

        if not event.wait(timeout=self._wait_timeout):
            raise RebunoError(f"Timed out waiting for approval (step_id={step_id})")

        return self._approval_data.get(step_id, {})

    def submit_tool(
        self,
        tool_id: str,
        arguments: Any = None,
        idempotency_key: str = "",
    ) -> str:
        """Submit a tool invocation without blocking for the result.

        Args:
            tool_id: The tool to invoke.
            arguments: Arguments to pass to the tool.
            idempotency_key: Optional key for deduplication.

        Returns:
            The step_id that can be used with await_steps.
        """
        if not idempotency_key:
            idempotency_key = f"{self.execution_id}:{tool_id}:{uuid.uuid4().hex[:8]}"

        local = self._is_local(tool_id)

        result = self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="invoke_tool",
            tool_id=tool_id,
            arguments=arguments,
            idempotency_key=idempotency_key,
            remote=not local,
        )

        if not result.accepted:
            raise PolicyError(result.error or "Intent denied by policy")

        if not result.step_id:
            raise RebunoError("No step_id returned for invoke_tool intent")

        if local:
            output = self._execute_local(result.step_id, tool_id, arguments)
            self._local_results[result.step_id] = output

        return result.step_id

    def await_steps(self, step_ids: list[str]) -> list[Any]:
        """Block until all given steps have completed and return their results.

        Args:
            step_ids: List of step IDs to wait for.

        Returns:
            List of results in the same order as step_ids.
        """
        results: dict[str, Any] = {}
        pending = set()

        for step_id in step_ids:
            if step_id in self._local_results:
                results[step_id] = self._local_results.pop(step_id)
            else:
                pending.add(step_id)

        for step_id in pending:
            event = self._result_events.get(step_id)
            if event is None:
                event = threading.Event()
                self._result_events[step_id] = event

            if not event.wait(timeout=self._wait_timeout):
                raise RebunoError(f"Timed out waiting for step: {step_id}")

            data = self._result_data.get(step_id, {})
            if data.get("status") == "failed":
                raise ToolError(
                    message=data.get("error", "Tool execution failed"),
                    step_id=step_id,
                )
            results[step_id] = data.get("result")

        return [results[sid] for sid in step_ids]

    def wait_signal(self, signal_type: str) -> Any:
        """Block until a signal of the given type is received.

        Args:
            signal_type: The signal type to wait for.

        Returns:
            The signal payload.

        Raises:
            PolicyError: If the wait intent is denied.
        """
        result = self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="wait",
            signal_type=signal_type,
        )
        if not result.accepted:
            raise PolicyError(result.error or "Wait intent denied")

        event = threading.Event()
        self._signal_events[signal_type] = event

        if signal_type in self._signal_data:
            event.set()

        if not event.wait(timeout=self._wait_timeout):
            raise RebunoError(f"Timed out waiting for signal: {signal_type}")

        return self._signal_data.get(signal_type)

    def complete(self, output: Any = None) -> None:
        """Mark the execution as completed with an optional output.

        Args:
            output: Optional output data for the execution.
        """
        self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="complete",
            output=output,
        )

    def fail(self, error: str) -> None:
        """Mark the execution as failed with an error message.

        Args:
            error: Description of the failure.
        """
        self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="fail",
            error=error,
        )


class BaseAgent(abc.ABC):
    """Base class for synchronous Rebuno agents.

    Subclass and implement the ``process`` method to define agent behavior.
    Call ``run()`` to start the agent's SSE event loop.

    Args:
        agent_id: Unique agent identifier.
        kernel_url: Base URL of the Rebuno kernel.
        api_key: Optional API key for authentication.
        consumer_id: Optional consumer ID (auto-generated if omitted).
        reconnect_delay: Initial reconnect delay in seconds.
        max_reconnect_delay: Maximum reconnect delay in seconds.
    """

    def __init__(
        self,
        agent_id: str,
        kernel_url: str,
        api_key: str = "",
        consumer_id: str = "",
        reconnect_delay: float = 3.0,
        max_reconnect_delay: float = 60.0,
    ):
        if not agent_id:
            raise ValueError("agent_id must not be empty")
        if not kernel_url:
            raise ValueError("kernel_url must not be empty")
        self.agent_id = agent_id
        self.consumer_id = consumer_id or f"{agent_id}-{uuid.uuid4().hex[:8]}"
        self.reconnect_delay = reconnect_delay
        self.max_reconnect_delay = max_reconnect_delay
        self._api_key = api_key
        self._client = RebunoClient(
            base_url=kernel_url,
            api_key=api_key,
        )
        self._tools: dict[str, Callable[..., Any]] = {}
        self._remote_tools: dict[str, Callable[..., Any]] = {}
        self._running = False

        self._exec_result_events: dict[str, dict[str, threading.Event]] = {}
        self._exec_result_data: dict[str, dict[str, dict[str, Any]]] = {}
        self._exec_signal_events: dict[str, dict[str, threading.Event]] = {}
        self._exec_signal_data: dict[str, dict[str, Any]] = {}
        self._exec_approval_events: dict[str, dict[str, threading.Event]] = {}
        self._exec_approval_data: dict[str, dict[str, dict[str, Any]]] = {}
        self._exec_lock = threading.Lock()

    def tool(self, tool_id: str) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        """Register a local tool function.

        Args:
            tool_id: Identifier for the tool.

        Returns:
            Decorator that registers the function.
        """
        def decorator(fn: Callable[..., Any]) -> Callable[..., Any]:
            self._tools[tool_id] = fn
            return fn
        return decorator

    def remote_tool(self, tool_id: str) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        """Register a remote tool function signature.

        Args:
            tool_id: Identifier for the tool.

        Returns:
            Decorator that registers the function.
        """
        def decorator(fn: Callable[..., Any]) -> Callable[..., Any]:
            self._remote_tools[tool_id] = fn
            return fn
        return decorator

    @abc.abstractmethod
    def process(self, ctx: AgentContext) -> Any:
        """Process an assigned execution.

        Args:
            ctx: The agent context for this execution.

        Returns:
            Output data to complete the execution with.
        """
        ...

    def run(self) -> None:
        """Start the agent event loop, connecting to the kernel SSE stream.

        Blocks until a shutdown signal is received or the agent is stopped.
        Automatically reconnects on connection failures with exponential backoff.
        """
        self._running = True

        def handle_signal(signum: int, frame: Any) -> None:
            logger.info("Shutdown signal received (signal=%d)", signum)
            self._running = False

        signal.signal(signal.SIGTERM, handle_signal)
        signal.signal(signal.SIGINT, handle_signal)

        logger.info(
            "Agent started: agent_id=%s consumer_id=%s",
            self.agent_id,
            self.consumer_id,
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
                logger.exception("SSE connection error, reconnecting")
                if self._running:
                    delay = jittered_backoff(
                        self.reconnect_delay, consecutive_failures, self.max_reconnect_delay,
                    )
                    time.sleep(delay)

        self._client.close()
        logger.info("Agent stopped")

    def _connect_and_process(self) -> None:
        params = {"agent_id": self.agent_id, "consumer_id": self.consumer_id}
        headers: dict[str, str] = {"Accept": "text/event-stream"}
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"

        with self._client._client.stream(
            "GET",
            "/v0/agents/stream",
            params=params,
            headers=headers,
            timeout=None,
        ) as response:
            response.raise_for_status()
            logger.info("SSE connection established")

            for event in parse_sse(response.iter_lines()):
                if not self._running:
                    return
                self._handle_event(event)

    def _notify_exec(
        self,
        data_registry: dict[str, dict[str, Any]],
        event_registry: dict[str, dict[str, threading.Event]],
        execution_id: str,
        key: str,
        value: Any,
    ) -> None:
        with self._exec_lock:
            store = data_registry.get(execution_id)
            events = event_registry.get(execution_id)
        if store is not None:
            store[key] = value
        if events is not None:
            evt = events.get(key)
            if evt is not None:
                evt.set()

    def _handle_event(self, event: SSEEvent) -> None:
        if event.type == "execution.assigned":
            data = json.loads(event.data)
            claim = ClaimResult(**data)
            thread = threading.Thread(
                target=self._handle_execution,
                args=(claim,),
                daemon=True,
            )
            thread.start()
        elif event.type == "tool.result":
            data = json.loads(event.data)
            self._notify_exec(
                self._exec_result_data, self._exec_result_events,
                data.get("execution_id", ""), data.get("step_id", ""), data,
            )
        elif event.type == "signal.received":
            data = json.loads(event.data)
            self._notify_exec(
                self._exec_signal_data, self._exec_signal_events,
                data.get("execution_id", ""), data.get("signal_type", ""), data.get("payload"),
            )
        elif event.type == "approval.resolved":
            data = json.loads(event.data)
            self._notify_exec(
                self._exec_approval_data, self._exec_approval_events,
                data.get("execution_id", ""), data.get("step_id", ""), data,
            )

    def _handle_execution(self, claim: ClaimResult) -> None:
        logger.info(
            "Execution assigned: execution_id=%s session_id=%s",
            claim.execution_id,
            claim.session_id,
        )

        result_events: dict[str, threading.Event] = {}
        result_data: dict[str, dict[str, Any]] = {}
        signal_events: dict[str, threading.Event] = {}
        signal_data: dict[str, Any] = {}
        approval_events: dict[str, threading.Event] = {}
        approval_data: dict[str, dict[str, Any]] = {}

        with self._exec_lock:
            self._exec_result_events[claim.execution_id] = result_events
            self._exec_result_data[claim.execution_id] = result_data
            self._exec_signal_events[claim.execution_id] = signal_events
            self._exec_signal_data[claim.execution_id] = signal_data
            self._exec_approval_events[claim.execution_id] = approval_events
            self._exec_approval_data[claim.execution_id] = approval_data

        ctx = AgentContext(
            self._client,
            claim,
            tools=self._tools,
            remote_tools=self._remote_tools,
            result_events=result_events,
            result_data=result_data,
            signal_events=signal_events,
            signal_data=signal_data,
            approval_events=approval_events,
            approval_data=approval_data,
        )

        try:
            output = self.process(ctx)
            self._client.submit_intent(
                execution_id=claim.execution_id,
                session_id=claim.session_id,
                intent_type="complete",
                output=output,
            )
            logger.info(
                "Execution completed: execution_id=%s", claim.execution_id
            )
        except (PolicyError, ToolError) as e:
            logger.warning(
                "Execution failed: execution_id=%s error=%s",
                claim.execution_id,
                str(e),
            )
            self._try_fail(claim.execution_id, claim.session_id, str(e))
        except Exception as e:
            logger.exception(
                "Process error: execution_id=%s", claim.execution_id
            )
            self._try_fail(claim.execution_id, claim.session_id, str(e))
        finally:
            with self._exec_lock:
                self._exec_result_events.pop(claim.execution_id, None)
                self._exec_result_data.pop(claim.execution_id, None)
                self._exec_signal_events.pop(claim.execution_id, None)
                self._exec_signal_data.pop(claim.execution_id, None)
                self._exec_approval_events.pop(claim.execution_id, None)
                self._exec_approval_data.pop(claim.execution_id, None)

    def _try_fail(self, execution_id: str, session_id: str, error: str) -> None:
        try:
            self._client.submit_intent(
                execution_id=execution_id,
                session_id=session_id,
                intent_type="fail",
                error=error,
            )
        except Exception:
            logger.exception(
                "Failed to submit fail intent: execution_id=%s", execution_id
            )
