import asyncio
import inspect
import json
from unittest.mock import AsyncMock, MagicMock

import pytest

from rebuno._internal import SSEEvent
from rebuno.async_agent import AsyncAgentContext, AsyncBaseAgent
from rebuno.async_client import AsyncRebunoClient
from rebuno.errors import PolicyError, RebunoError, ToolError
from rebuno.models import IntentResult

from conftest import make_claim


async def _simulate_tool_result(result_events, result_data, step_id, data, delay=0.05):
    await asyncio.sleep(delay)
    result_data[step_id] = data
    evt = result_events.get(step_id)
    if evt:
        evt.set()


async def _simulate_signal(signal_events, signal_data, signal_type, payload, delay=0.05):
    await asyncio.sleep(delay)
    signal_data[signal_type] = payload
    evt = signal_events.get(signal_type)
    if evt:
        evt.set()


class TestAsyncAgentContext:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.mock_client.report_step_result = AsyncMock()
        self.claim = make_claim()
        self.result_events = {}
        self.result_data = {}
        self.signal_events = {}
        self.signal_data = {}
        self.ctx = AsyncAgentContext(
            self.mock_client,
            self.claim,
            result_events=self.result_events,
            result_data=self.result_data,
            signal_events=self.signal_events,
            signal_data=self.signal_data,
            wait_timeout=2.0,
        )

    @pytest.mark.asyncio
    async def test_invoke_tool_remote_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        asyncio.create_task(_simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "succeeded", "result": {"data": "found"}},
        ))
        result = await self.ctx.invoke_tool("web.search", {"q": "test"})
        assert result == {"data": "found"}

    @pytest.mark.asyncio
    async def test_invoke_tool_policy_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="denied"
        )

        with pytest.raises(PolicyError):
            await self.ctx.invoke_tool("web.search")

    @pytest.mark.asyncio
    async def test_invoke_tool_no_step_id(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id=""
        )

        with pytest.raises(RebunoError, match="No step_id"):
            await self.ctx.invoke_tool("web.search")

    @pytest.mark.asyncio
    async def test_invoke_tool_remote_failed(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        asyncio.create_task(_simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "failed", "error": "tool crashed"},
        ))
        with pytest.raises(ToolError) as exc_info:
            await self.ctx.invoke_tool("web.search")
        assert exc_info.value.tool_id == "web.search"

    @pytest.mark.asyncio
    async def test_invoke_tool_remote_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            result_events={}, result_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out"):
            await ctx.invoke_tool("web.search")

    @pytest.mark.asyncio
    async def test_submit_tool_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        step_id = await self.ctx.submit_tool("web.search")
        assert step_id == "step-1"

    @pytest.mark.asyncio
    async def test_submit_tool_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="denied"
        )

        with pytest.raises(PolicyError):
            await self.ctx.submit_tool("web.search")

    @pytest.mark.asyncio
    async def test_await_steps_remote_success(self):
        asyncio.create_task(_simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "succeeded", "result": "r1"},
        ))
        asyncio.create_task(_simulate_tool_result(
            self.result_events, self.result_data, "step-2",
            {"status": "succeeded", "result": "r2"},
            delay=0.06,
        ))
        results = await self.ctx.await_steps(["step-1", "step-2"])
        assert results == ["r1", "r2"]

    @pytest.mark.asyncio
    async def test_await_steps_remote_failed(self):
        asyncio.create_task(_simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "failed", "error": "crash"},
        ))
        with pytest.raises(ToolError, match="crash"):
            await self.ctx.await_steps(["step-1"])

    @pytest.mark.asyncio
    async def test_wait_signal_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(accepted=True)
        asyncio.create_task(_simulate_signal(
            self.signal_events, self.signal_data, "approval",
            {"ok": True},
        ))
        result = await self.ctx.wait_signal("approval")
        assert result == {"ok": True}

    @pytest.mark.asyncio
    async def test_wait_signal_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="denied"
        )

        with pytest.raises(PolicyError):
            await self.ctx.wait_signal("approval")

    @pytest.mark.asyncio
    async def test_wait_signal_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(accepted=True)
        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            signal_events={}, signal_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out"):
            await ctx.wait_signal("approval")


class TestAsyncBaseAgent:
    @pytest.mark.asyncio
    async def test_handle_execution_completes(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {"answer": "42"}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        agent._client.submit_intent = AsyncMock(return_value=IntentResult(accepted=True))

        await agent._handle_execution(make_claim())

        call_kwargs = agent._client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "complete"
        assert call_kwargs["output"] == {"answer": "42"}

    @pytest.mark.asyncio
    async def test_handle_execution_process_error(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                raise RuntimeError("boom")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        agent._client.submit_intent = AsyncMock(return_value=IntentResult(accepted=True))

        await agent._handle_execution(make_claim())

        call_kwargs = agent._client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "fail"
        assert "boom" in call_kwargs["error"]


class TestGetTools:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.mock_client.report_step_result = AsyncMock()
        self.claim = make_claim()

    @pytest.mark.asyncio
    async def test_wraps_local_tool(self):
        async def search(query: str, limit: int = 10) -> dict:
            """Search the web."""
            return {"results": [query], "limit": limit}

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        tools = ctx.get_tools()
        assert len(tools) == 1

        result = await tools[0]("test query")
        assert result == {"results": ["test query"], "limit": 10}

        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["tool_id"] == "web.search"
        assert call_kwargs["arguments"] == {"query": "test query", "limit": 10}

        self.mock_client.report_step_result.assert_called_once()
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is True

    @pytest.mark.asyncio
    async def test_policy_denied(self):
        async def search(query: str) -> dict:
            return {}

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="blocked"
        )

        tools = ctx.get_tools()
        with pytest.raises(PolicyError, match="blocked"):
            await tools[0]("test")

    def test_preserves_signature(self):
        async def search(query: str, limit: int = 10) -> dict:
            """Search the web for results."""
            return {}

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        tools = ctx.get_tools()
        wrapper = tools[0]

        assert wrapper.__name__ == "search"
        assert wrapper.__doc__ == "Search the web for results."
        sig = inspect.signature(wrapper)
        assert list(sig.parameters.keys()) == ["query", "limit"]
        assert sig.parameters["limit"].default == 10

    @pytest.mark.asyncio
    async def test_wraps_remote_tool(self):
        async def search(query: str) -> dict:
            """Search the web."""
            ...

        result_events = {}
        result_data = {}
        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            remote_tools={"web.search": search},
            result_events=result_events,
            result_data=result_data,
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        asyncio.create_task(_simulate_tool_result(
            result_events, result_data, "step-1",
            {"status": "succeeded", "result": {"data": "found"}},
        ))

        tools = ctx.get_tools()
        assert len(tools) == 1

        result = await tools[0]("test query")
        assert result == {"data": "found"}

        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["tool_id"] == "web.search"
        assert call_kwargs["arguments"] == {"query": "test query"}
        assert call_kwargs["remote"] is True

    @pytest.mark.asyncio
    async def test_remote_tool_policy_denied(self):
        async def search(query: str) -> dict:
            ...

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, remote_tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="blocked"
        )

        tools = ctx.get_tools()
        with pytest.raises(PolicyError, match="blocked"):
            await tools[0]("test")

    def test_includes_both_local_and_remote(self):
        async def local_search(query: str) -> dict:
            """Local search."""
            return {}

        async def remote_fetch(url: str) -> dict:
            """Remote fetch."""
            ...

        ctx = AsyncAgentContext(
            self.mock_client,
            self.claim,
            tools={"web.search": local_search},
            remote_tools={"doc.fetch": remote_fetch},
        )
        tools = ctx.get_tools()
        assert len(tools) == 2
        names = {t.__name__ for t in tools}
        assert names == {"local_search", "remote_fetch"}


class TestAsyncBaseAgentMcp:
    @pytest.mark.asyncio
    async def test_mcp_server_adds_to_config(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {}

        agent = TestAgent(agent_id="test", kernel_url="http://localhost:8080")
        agent.mcp_server("fs", command="python", args=["server.py"])
        agent.mcp_server("github", url="http://localhost:8000/mcp")

        assert agent._mcp is not None
        assert "fs" in agent._mcp._connections
        assert "github" in agent._mcp._connections


class TestAsyncBaseAgentInit:
    def test_empty_agent_id_raises(self):
        with pytest.raises(ValueError, match="agent_id"):
            class A(AsyncBaseAgent):
                async def process(self, ctx):
                    return {}
            A(agent_id="", kernel_url="http://localhost:8080")

    def test_empty_kernel_url_raises(self):
        with pytest.raises(ValueError, match="kernel_url"):
            class A(AsyncBaseAgent):
                async def process(self, ctx):
                    return {}
            A(agent_id="agent-1", kernel_url="")

    def test_tool_decorator_registers(self):
        class A(AsyncBaseAgent):
            async def process(self, ctx):
                return {}

        agent = A(agent_id="agent-1", kernel_url="http://localhost:8080")

        @agent.tool("my.tool")
        async def my_tool(x: int) -> int:
            return x + 1

        assert "my.tool" in agent._tools
        assert agent._tools["my.tool"] is my_tool

    def test_remote_tool_decorator_registers(self):
        class A(AsyncBaseAgent):
            async def process(self, ctx):
                return {}

        agent = A(agent_id="agent-1", kernel_url="http://localhost:8080")

        @agent.remote_tool("ext.tool")
        async def ext_tool(url: str) -> dict:
            ...

        assert "ext.tool" in agent._remote_tools


class TestAsyncBaseAgentExecution:
    @pytest.mark.asyncio
    async def test_handle_execution_cleans_up_storage(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {"ok": True}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        agent._client.submit_intent = AsyncMock(return_value=IntentResult(accepted=True))

        claim = make_claim()
        await agent._handle_execution(claim)

        assert claim.execution_id not in agent._exec_result_events
        assert claim.execution_id not in agent._exec_result_data
        assert claim.execution_id not in agent._exec_signal_events
        assert claim.execution_id not in agent._exec_signal_data

    @pytest.mark.asyncio
    async def test_handle_execution_cleans_up_on_error(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                raise RuntimeError("crash")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        agent._client.submit_intent = AsyncMock(return_value=IntentResult(accepted=True))

        claim = make_claim()
        await agent._handle_execution(claim)

        assert claim.execution_id not in agent._exec_result_events

    @pytest.mark.asyncio
    async def test_try_fail_suppresses_exception(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                raise RuntimeError("crash")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        agent._client.submit_intent = AsyncMock(side_effect=Exception("network down"))

        claim = make_claim()
        # Should not raise even though _try_fail itself fails
        await agent._handle_execution(claim)


class TestAsyncHandleEvent:
    @pytest.mark.asyncio
    async def test_tool_result_dispatched(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)

        eid = "exec-1"
        result_events = {}
        result_data = {}
        evt = asyncio.Event()
        result_events["step-1"] = evt
        agent._exec_result_events[eid] = result_events
        agent._exec_result_data[eid] = result_data

        event = SSEEvent(
            type="tool.result",
            data=json.dumps({
                "execution_id": "exec-1",
                "step_id": "step-1",
                "status": "succeeded",
                "result": {"data": "ok"},
            }),
        )
        await agent._handle_event(event)

        assert evt.is_set()
        assert result_data["step-1"]["status"] == "succeeded"

    @pytest.mark.asyncio
    async def test_signal_received_dispatched(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)

        eid = "exec-1"
        signal_events = {}
        signal_data = {}
        evt = asyncio.Event()
        signal_events["approval"] = evt
        agent._exec_signal_events[eid] = signal_events
        agent._exec_signal_data[eid] = signal_data

        event = SSEEvent(
            type="signal.received",
            data=json.dumps({
                "execution_id": "exec-1",
                "signal_type": "approval",
                "payload": {"approved": True},
            }),
        )
        await agent._handle_event(event)

        assert evt.is_set()
        assert signal_data["approval"] == {"approved": True}


class TestAsyncHandleEventEdgeCases:
    def _make_agent(self):
        class TestAgent(AsyncBaseAgent):
            async def process(self, ctx):
                return {}
        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=AsyncRebunoClient)
        return agent

    @pytest.mark.asyncio
    async def test_tool_result_unknown_execution_id_ignored(self):
        """When a tool.result event arrives for an execution_id that has no
        entry in the agent's internal registries, it should be silently
        ignored without raising a KeyError or crashing."""
        agent = self._make_agent()

        event = SSEEvent(
            type="tool.result",
            data=json.dumps({
                "execution_id": "unknown-exec",
                "step_id": "step-1",
                "status": "succeeded",
                "result": {"data": "ok"},
            }),
        )
        # Should not raise
        await agent._handle_event(event)

    @pytest.mark.asyncio
    async def test_signal_received_unknown_execution_id_ignored(self):
        """When a signal.received event arrives for an unknown execution_id,
        it should be silently ignored."""
        agent = self._make_agent()

        event = SSEEvent(
            type="signal.received",
            data=json.dumps({
                "execution_id": "unknown-exec",
                "signal_type": "approval",
                "payload": {"approved": True},
            }),
        )
        # Should not raise
        await agent._handle_event(event)

    @pytest.mark.asyncio
    async def test_approval_resolved_unknown_execution_id_ignored(self):
        """When an approval.resolved event arrives for an unknown execution_id,
        it should be silently ignored."""
        agent = self._make_agent()

        event = SSEEvent(
            type="approval.resolved",
            data=json.dumps({
                "execution_id": "unknown-exec",
                "step_id": "step-1",
                "approved": True,
            }),
        )
        # Should not raise
        await agent._handle_event(event)

    @pytest.mark.asyncio
    async def test_execution_assigned_malformed_json_raises(self):
        """When an execution.assigned SSE event has invalid JSON data,
        json.loads raises a JSONDecodeError. The current implementation does
        not catch this, so it propagates up to the SSE processing loop."""
        agent = self._make_agent()

        event = SSEEvent(
            type="execution.assigned",
            data="not valid json {{{",
        )
        with pytest.raises(json.JSONDecodeError):
            await agent._handle_event(event)


class TestAsyncInvokeToolEdgeCases:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.mock_client.report_step_result = AsyncMock()
        self.claim = make_claim()

    @pytest.mark.asyncio
    async def test_invoke_local_tool_directly(self):
        async def add(a: int, b: int) -> int:
            return a + b

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"math.add": add}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        result = await ctx.invoke_tool("math.add", {"a": 1, "b": 2})
        assert result == 3

        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is True
        assert report_kwargs["data"] == 3

    @pytest.mark.asyncio
    async def test_invoke_sync_local_tool(self):
        """Async context should handle sync local tools via _execute_local."""
        def add_sync(a: int, b: int) -> int:
            return a + b

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"math.add": add_sync}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        result = await ctx.invoke_tool("math.add", {"a": 5, "b": 6})
        assert result == 11

    @pytest.mark.asyncio
    async def test_invoke_local_tool_with_non_dict_arguments(self):
        async def noop() -> dict:
            return {"ok": True}

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"noop": noop}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        result = await ctx.invoke_tool("noop", "not-a-dict")
        assert result == {"ok": True}

    @pytest.mark.asyncio
    async def test_invoke_local_tool_error_raises_tool_error(self):
        async def fail_tool() -> dict:
            raise RuntimeError("broken")

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"bad.tool": fail_tool}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        with pytest.raises(ToolError, match="broken") as exc_info:
            await ctx.invoke_tool("bad.tool")
        assert exc_info.value.tool_id == "bad.tool"
        assert exc_info.value.step_id == "step-1"

        # Should report failure
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is False

    @pytest.mark.asyncio
    async def test_invoke_local_tool_raises_tool_error_directly(self):
        """When a local tool raises a ToolError directly, it should be
        re-raised as-is without being double-wrapped in another ToolError."""
        original_error = ToolError(
            message="custom tool error",
            tool_id="custom.tool",
            step_id="original-step",
            retryable=True,
        )

        async def tool_that_raises_tool_error() -> dict:
            raise original_error

        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            tools={"my.tool": tool_that_raises_tool_error},
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        with pytest.raises(ToolError) as exc_info:
            await ctx.invoke_tool("my.tool")

        # The raised error should be the original ToolError, not a new one
        assert exc_info.value is original_error
        assert exc_info.value.tool_id == "custom.tool"
        assert exc_info.value.step_id == "original-step"
        assert exc_info.value.retryable is True

        # Should still report failure to the kernel
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is False

    @pytest.mark.asyncio
    async def test_pending_approval_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1", pending_approval=True,
        )
        approval_events = {}
        approval_data = {}
        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            approval_events=approval_events,
            approval_data=approval_data,
            wait_timeout=2.0,
        )

        async def _push_approval():
            await asyncio.sleep(0.05)
            approval_data["step-1"] = {"approved": False}
            evt = approval_events.get("step-1")
            if evt:
                evt.set()

        asyncio.create_task(_push_approval())

        with pytest.raises(PolicyError, match="denied by human approval"):
            await ctx.invoke_tool("web.search")

    @pytest.mark.asyncio
    async def test_pending_approval_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1", pending_approval=True,
        )
        ctx = AsyncAgentContext(
            self.mock_client, self.claim,
            approval_events={}, approval_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out waiting for approval"):
            await ctx.invoke_tool("web.search")


class TestAsyncSubmitToolEdgeCases:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.mock_client.report_step_result = AsyncMock()
        self.claim = make_claim()

    @pytest.mark.asyncio
    async def test_submit_local_then_await(self):
        async def add(a: int, b: int) -> int:
            return a + b

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"math.add": add}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        step_id = await ctx.submit_tool("math.add", {"a": 3, "b": 4})
        results = await ctx.await_steps([step_id])
        assert results == [7]


class TestAsyncGetToolsEdgeCases:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.mock_client.report_step_result = AsyncMock()
        self.claim = make_claim()

    @pytest.mark.asyncio
    async def test_local_tool_exception_reports_failure(self):
        async def bad_tool(query: str) -> dict:
            raise ValueError("bad input")

        ctx = AsyncAgentContext(
            self.mock_client, self.claim, tools={"web.search": bad_tool}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        tools = ctx.get_tools()
        with pytest.raises(ToolError, match="bad input"):
            await tools[0]("test")

        self.mock_client.report_step_result.assert_called_once()
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is False
        assert "bad input" in report_kwargs["error"]


class TestAsyncContextCompleteAndFail:
    def setup_method(self):
        self.mock_client = MagicMock(spec=AsyncRebunoClient)
        self.mock_client.submit_intent = AsyncMock()
        self.claim = make_claim()

    @pytest.mark.asyncio
    async def test_complete_submits_intent(self):
        ctx = AsyncAgentContext(self.mock_client, self.claim)
        await ctx.complete({"answer": 42})
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "complete"
        assert call_kwargs["output"] == {"answer": 42}

    @pytest.mark.asyncio
    async def test_fail_submits_intent(self):
        ctx = AsyncAgentContext(self.mock_client, self.claim)
        await ctx.fail("something went wrong")
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "fail"
        assert call_kwargs["error"] == "something went wrong"

