from __future__ import annotations

import abc
import asyncio
import functools
import inspect
import json
import logging
import signal
import uuid
from collections.abc import Callable
from typing import Any

from rebuno._internal import SSEEvent, async_parse_sse, jittered_backoff
from rebuno.async_client import AsyncRebunoClient
from rebuno.errors import PolicyError, RebunoError, ToolError
from rebuno.mcp import McpManager
from rebuno.models import ClaimResult, HistoryEntry

logger = logging.getLogger("rebuno.agent")


class AsyncAgentContext:
    """Context for a single asynchronous agent execution.

    Provides async methods to invoke tools, wait for signals, and manage
    step results. Each execution gets its own isolated event/data storage
    to prevent concurrent executions from interfering with each other.

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
        client: AsyncRebunoClient,
        claim: ClaimResult,
        tools: dict[str, Callable[..., Any]] | None = None,
        remote_tools: dict[str, Callable[..., Any]] | None = None,
        result_events: dict[str, asyncio.Event] | None = None,
        result_data: dict[str, dict[str, Any]] | None = None,
        signal_events: dict[str, asyncio.Event] | None = None,
        signal_data: dict[str, Any] | None = None,
        approval_events: dict[str, asyncio.Event] | None = None,
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

    async def invoke_tool(
        self,
        tool_id: str,
        arguments: Any = None,
        idempotency_key: str = "",
    ) -> Any:
        """Invoke a tool and await the result.

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

        result = await self._client.submit_intent(
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
            approval = await self._wait_for_approval(step_id)
            if not approval.get("approved", False):
                raise PolicyError("Tool invocation denied by human approval")

        if local:
            return await self._execute_local(step_id, tool_id, arguments)
        return await self._wait_for_result(step_id, tool_id)

    def get_tools(self) -> list[Callable]:
        """Return wrapped async callables for all registered tools (local and remote).

        Each wrapper invokes the tool through the kernel intent flow.
        """
        all_tools = {**self._tools, **self._remote_tools}
        return [self._wrap_tool(tid, fn) for tid, fn in all_tools.items()]

    def _wrap_tool(self, tool_id: str, fn: Callable[..., Any]) -> Callable[..., Any]:
        ctx = self

        @functools.wraps(fn)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            sig = inspect.signature(fn)
            bound = sig.bind(*args, **kwargs)
            bound.apply_defaults()
            return await ctx.invoke_tool(tool_id, dict(bound.arguments))

        return wrapper

    async def _execute_local(self, step_id: str, tool_id: str, arguments: Any) -> Any:
        fn = self._tools[tool_id]
        if arguments is not None and not isinstance(arguments, dict):
            logger.warning("Tool %s received non-dict arguments (%s), using {}", tool_id, type(arguments).__name__)
        kwargs = arguments if isinstance(arguments, dict) else {}

        try:
            if inspect.iscoroutinefunction(fn):
                output = await fn(**kwargs)
            else:
                output = fn(**kwargs)
        except Exception as e:
            await self._client.report_step_result(
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

        await self._client.report_step_result(
            execution_id=self.execution_id,
            session_id=self.session_id,
            step_id=step_id,
            success=True,
            data=output,
        )
        return output

    async def _wait_for_event(
        self,
        key: str,
        events: dict[str, asyncio.Event],
        data: dict[str, Any],
        timeout_msg: str,
    ) -> Any:
        event = asyncio.Event()
        events[key] = event

        if key in data:
            event.set()

        try:
            await asyncio.wait_for(event.wait(), timeout=self._wait_timeout)
        except asyncio.TimeoutError:
            raise RebunoError(timeout_msg)

        return data.get(key, {})

    async def _wait_for_result(self, step_id: str, tool_id: str) -> Any:
        data = await self._wait_for_event(
            step_id, self._result_events, self._result_data,
            f"Timed out waiting for tool result (step_id={step_id})",
        )
        if data.get("status") == "failed":
            raise ToolError(
                message=data.get("error", "Tool execution failed"),
                tool_id=tool_id,
                step_id=step_id,
            )
        return data.get("result")

    async def _wait_for_approval(self, step_id: str) -> dict[str, Any]:
        return await self._wait_for_event(
            step_id, self._approval_events, self._approval_data,
            f"Timed out waiting for approval (step_id={step_id})",
        )

    async def submit_tool(
        self,
        tool_id: str,
        arguments: Any = None,
        idempotency_key: str = "",
    ) -> str:
        """Submit a tool invocation without awaiting the result.

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

        result = await self._client.submit_intent(
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
            output = await self._execute_local(result.step_id, tool_id, arguments)
            self._local_results[result.step_id] = output

        return result.step_id

    async def await_steps(self, step_ids: list[str]) -> list[Any]:
        """Await completion of multiple steps concurrently.

        Args:
            step_ids: List of step IDs to wait for.

        Returns:
            List of results in the same order as step_ids.
        """
        results: dict[str, Any] = {}
        remote_ids: list[str] = []

        for step_id in step_ids:
            if step_id in self._local_results:
                results[step_id] = self._local_results.pop(step_id)
            else:
                remote_ids.append(step_id)

        if remote_ids:
            async def _wait_one(sid: str) -> tuple[str, Any]:
                data = await self._wait_for_event(
                    sid, self._result_events, self._result_data,
                    f"Timed out waiting for tool result (step_id={sid})",
                )
                if data.get("status") == "failed":
                    raise ToolError(
                        message=data.get("error", "Tool execution failed"),
                        step_id=sid,
                    )
                return sid, data.get("result")

            remote_results = await asyncio.gather(*[_wait_one(sid) for sid in remote_ids])
            for sid, val in remote_results:
                results[sid] = val

        return [results[sid] for sid in step_ids]

    async def wait_signal(self, signal_type: str) -> Any:
        """Await a signal of the given type.

        Args:
            signal_type: The signal type to wait for.

        Returns:
            The signal payload.

        Raises:
            PolicyError: If the wait intent is denied.
        """
        result = await self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="wait",
            signal_type=signal_type,
        )
        if not result.accepted:
            raise PolicyError(result.error or "Wait intent denied")

        return await self._wait_for_event(
            signal_type, self._signal_events, self._signal_data,
            f"Timed out waiting for signal: {signal_type}",
        )

    async def complete(self, output: Any = None) -> None:
        """Mark the execution as completed with an optional output.

        Args:
            output: Optional output data for the execution.
        """
        await self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="complete",
            output=output,
        )

    async def fail(self, error: str) -> None:
        """Mark the execution as failed with an error message.

        Args:
            error: Description of the failure.
        """
        await self._client.submit_intent(
            execution_id=self.execution_id,
            session_id=self.session_id,
            intent_type="fail",
            error=error,
        )


class AsyncBaseAgent(abc.ABC):
    """Base class for asynchronous Rebuno agents.

    Subclass and implement the ``process`` method to define agent behavior.
    Call ``await run()`` to start the agent's SSE event loop.

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
        self._client = AsyncRebunoClient(
            base_url=kernel_url,
            api_key=api_key,
        )
        self._tools: dict[str, Callable[..., Any]] = {}
        self._remote_tools: dict[str, Callable[..., Any]] = {}
        self._running = False
        self._shutdown_event = asyncio.Event()
        self._connect_task: asyncio.Task[None] | None = None

        self._exec_result_events: dict[str, dict[str, asyncio.Event]] = {}
        self._exec_result_data: dict[str, dict[str, dict[str, Any]]] = {}
        self._exec_signal_events: dict[str, dict[str, asyncio.Event]] = {}
        self._exec_signal_data: dict[str, dict[str, Any]] = {}
        self._exec_approval_events: dict[str, dict[str, asyncio.Event]] = {}
        self._exec_approval_data: dict[str, dict[str, dict[str, Any]]] = {}
        self._mcp: McpManager | None = None

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
        """Register an MCP server whose tools will be available as local tools.

        Args:
            name: Display name for the server.
            command: Command to start a stdio MCP server.
            args: Arguments for the command.
            env: Environment variables for the subprocess.
            url: URL for an HTTP MCP server.
            headers: HTTP headers for the connection.
            prefix: Tool ID prefix (defaults to name).
        """
        if self._mcp is None:
            self._mcp = McpManager()
        self._mcp.add_server(
            name, command=command, args=args, env=env,
            url=url, headers=headers, prefix=prefix,
        )

    def mcp_servers_from_config(self, config: dict[str, Any]) -> None:
        """Register MCP servers from a standard mcpServers config dict.

        Args:
            config: Dict with "mcpServers" key or direct server mapping.
        """
        if self._mcp is None:
            self._mcp = McpManager()
        self._mcp.add_servers_from_config(config)

    @abc.abstractmethod
    async def process(self, ctx: AsyncAgentContext) -> Any:
        """Process an assigned execution.

        Args:
            ctx: The async agent context for this execution.

        Returns:
            Output data to complete the execution with.
        """
        ...

    async def run(self) -> None:
        """Start the async agent event loop, connecting to the kernel SSE stream.

        Blocks until a shutdown signal is received or the agent is stopped.
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
            "Agent started: agent_id=%s consumer_id=%s",
            self.agent_id,
            self.consumer_id,
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
                    logger.exception("SSE connection error, reconnecting")
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
            logger.info("Agent stopped")

    def _handle_shutdown(self) -> None:
        logger.info("Shutdown signal received")
        self._running = False
        self._shutdown_event.set()
        if self._connect_task and not self._connect_task.done():
            self._connect_task.cancel()

    async def _connect_and_process(self) -> None:
        params = {"agent_id": self.agent_id, "consumer_id": self.consumer_id}
        headers: dict[str, str] = {"Accept": "text/event-stream"}
        if self._api_key:
            headers["Authorization"] = f"Bearer {self._api_key}"

        async with self._client._client.stream(
            "GET",
            "/v0/agents/stream",
            params=params,
            headers=headers,
            timeout=None,
        ) as response:
            response.raise_for_status()
            logger.info("SSE connection established")

            async for event in async_parse_sse(response.aiter_lines()):
                if not self._running:
                    return
                await self._handle_event(event)

    def _dispatch_event_data(
        self,
        exec_data: dict[str, dict[str, Any]],
        exec_events: dict[str, dict[str, asyncio.Event]],
        execution_id: str,
        key: str,
        value: Any,
    ) -> None:
        data_store = exec_data.get(execution_id)
        if data_store is not None:
            data_store[key] = value
        event_store = exec_events.get(execution_id)
        if event_store is not None:
            evt = event_store.get(key)
            if evt is not None:
                evt.set()

    async def _handle_event(self, event: SSEEvent) -> None:
        if event.type == "execution.assigned":
            data = json.loads(event.data)
            claim = ClaimResult(**data)
            task = asyncio.create_task(self._handle_execution(claim))
            task.add_done_callback(lambda t: t.exception() if not t.cancelled() else None)
        elif event.type == "tool.result":
            data = json.loads(event.data)
            self._dispatch_event_data(
                self._exec_result_data, self._exec_result_events,
                data.get("execution_id", ""), data.get("step_id", ""), data,
            )
        elif event.type == "signal.received":
            data = json.loads(event.data)
            self._dispatch_event_data(
                self._exec_signal_data, self._exec_signal_events,
                data.get("execution_id", ""), data.get("signal_type", ""), data.get("payload"),
            )
        elif event.type == "approval.resolved":
            data = json.loads(event.data)
            self._dispatch_event_data(
                self._exec_approval_data, self._exec_approval_events,
                data.get("execution_id", ""), data.get("step_id", ""), data,
            )

    async def _handle_execution(self, claim: ClaimResult) -> None:
        logger.info(
            "Execution assigned: execution_id=%s session_id=%s",
            claim.execution_id,
            claim.session_id,
        )

        eid = claim.execution_id
        result_events = self._exec_result_events[eid] = {}
        result_data = self._exec_result_data[eid] = {}
        signal_events = self._exec_signal_events[eid] = {}
        signal_data = self._exec_signal_data[eid] = {}
        approval_events = self._exec_approval_events[eid] = {}
        approval_data = self._exec_approval_data[eid] = {}

        mcp_tools: dict[str, Callable[..., Any]] = {}
        if self._mcp is not None:
            if not any(c.connected for c in self._mcp._connections.values()):
                await self._mcp.connect_all()
            raw_tools = await self._mcp.all_tools()
            for t in raw_tools:
                tool_id = t["id"]
                schema = t.get("input_schema", {})
                props = schema.get("properties", {})

                params = [
                    inspect.Parameter(p, inspect.Parameter.KEYWORD_ONLY)
                    for p in props
                ]
                async def _mcp_call(__tid: str = tool_id, **kwargs: Any) -> Any:
                    return await self._mcp.call_tool(__tid, kwargs)  # type: ignore[union-attr]

                _mcp_call.__name__ = t["name"]
                _mcp_call.__doc__ = t.get("description", "")
                _mcp_call.__signature__ = inspect.Signature(params)  # type: ignore[attr-defined]
                mcp_tools[tool_id] = _mcp_call

        ctx = AsyncAgentContext(
            self._client,
            claim,
            tools={**self._tools, **mcp_tools},
            remote_tools=self._remote_tools,
            result_events=result_events,
            result_data=result_data,
            signal_events=signal_events,
            signal_data=signal_data,
            approval_events=approval_events,
            approval_data=approval_data,
        )

        try:
            output = await self.process(ctx)
            await self._client.submit_intent(
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
            await self._try_fail(claim.execution_id, claim.session_id, str(e))
        except Exception as e:
            logger.exception(
                "Process error: execution_id=%s", claim.execution_id
            )
            await self._try_fail(claim.execution_id, claim.session_id, str(e))
        finally:
            for store in (
                self._exec_result_events, self._exec_result_data,
                self._exec_signal_events, self._exec_signal_data,
                self._exec_approval_events, self._exec_approval_data,
            ):
                store.pop(eid, None)

    async def _try_fail(self, execution_id: str, session_id: str, error: str) -> None:
        try:
            await self._client.submit_intent(
                execution_id=execution_id,
                session_id=session_id,
                intent_type="fail",
                error=error,
            )
        except Exception:
            logger.exception(
                "Failed to submit fail intent: execution_id=%s", execution_id
            )
