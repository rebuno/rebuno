import inspect
import json
import threading
import time
from unittest.mock import MagicMock

import pytest

from rebuno._internal import SSEEvent
from rebuno.agent import AgentContext, BaseAgent
from rebuno.client import RebunoClient
from rebuno.errors import PolicyError, RebunoError, ToolError
from rebuno.models import IntentResult

from conftest import make_claim


def _simulate_tool_result(result_events, result_data, step_id, data, delay=0.05):
    def _push():
        deadline = time.monotonic() + 5.0
        while step_id not in result_events:
            if time.monotonic() > deadline:
                return
            time.sleep(0.01)
        time.sleep(delay)
        result_data[step_id] = data
        evt = result_events.get(step_id)
        if evt:
            evt.set()
    threading.Thread(target=_push, daemon=True).start()


def _simulate_signal(signal_events, signal_data, signal_type, payload, delay=0.05):
    def _push():
        deadline = time.monotonic() + 5.0
        while signal_type not in signal_events:
            if time.monotonic() > deadline:
                return
            time.sleep(0.01)
        time.sleep(delay)
        signal_data[signal_type] = payload
        evt = signal_events.get(signal_type)
        if evt:
            evt.set()
    threading.Thread(target=_push, daemon=True).start()


class TestAgentContext:
    def setup_method(self):
        self.mock_client = MagicMock(spec=RebunoClient)
        self.claim = make_claim()
        self.result_events = {}
        self.result_data = {}
        self.signal_events = {}
        self.signal_data = {}
        self.ctx = AgentContext(
            self.mock_client,
            self.claim,
            result_events=self.result_events,
            result_data=self.result_data,
            signal_events=self.signal_events,
            signal_data=self.signal_data,
            wait_timeout=2.0,
        )

    def test_invoke_tool_remote_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        _simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "succeeded", "result": {"data": "found"}},
        )
        result = self.ctx.invoke_tool("web.search", {"q": "test"})
        assert result == {"data": "found"}

    def test_invoke_tool_policy_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="policy denied"
        )

        with pytest.raises(PolicyError) as exc_info:
            self.ctx.invoke_tool("web.search")
        assert "policy denied" in str(exc_info.value)

    def test_invoke_tool_no_step_id(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id=""
        )

        with pytest.raises(RebunoError, match="No step_id"):
            self.ctx.invoke_tool("web.search")

    def test_invoke_tool_remote_failed(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        _simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "failed", "error": "tool crashed"},
        )
        with pytest.raises(ToolError) as exc_info:
            self.ctx.invoke_tool("web.search")
        assert "tool crashed" in str(exc_info.value)
        assert exc_info.value.tool_id == "web.search"
        assert exc_info.value.step_id == "step-1"

    def test_invoke_tool_remote_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        ctx = AgentContext(
            self.mock_client, self.claim,
            result_events={}, result_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out"):
            ctx.invoke_tool("web.search")

    def test_invoke_tool_custom_idempotency_key(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        _simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "succeeded", "result": "ok"},
        )
        self.ctx.invoke_tool("web.search", idempotency_key="my-key")
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["idempotency_key"] == "my-key"

    def test_submit_tool_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        step_id = self.ctx.submit_tool("web.search", {"q": "test"})
        assert step_id == "step-1"

    def test_submit_tool_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="denied"
        )

        with pytest.raises(PolicyError):
            self.ctx.submit_tool("web.search")

    def test_await_steps_remote_success(self):
        _simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "succeeded", "result": "r1"},
        )
        _simulate_tool_result(
            self.result_events, self.result_data, "step-2",
            {"status": "succeeded", "result": "r2"},
            delay=0.06,
        )
        results = self.ctx.await_steps(["step-1", "step-2"])
        assert results == ["r1", "r2"]

    def test_await_steps_remote_failed(self):
        _simulate_tool_result(
            self.result_events, self.result_data, "step-1",
            {"status": "failed", "error": "step crashed"},
        )
        with pytest.raises(ToolError, match="step crashed"):
            self.ctx.await_steps(["step-1"])

    def test_await_steps_remote_timeout(self):
        ctx = AgentContext(
            self.mock_client, self.claim,
            result_events={}, result_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out"):
            ctx.await_steps(["step-1"])

    def test_wait_signal_success(self):
        self.mock_client.submit_intent.return_value = IntentResult(accepted=True)
        _simulate_signal(
            self.signal_events, self.signal_data, "approval",
            {"approved": True},
        )
        result = self.ctx.wait_signal("approval")
        assert result == {"approved": True}
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "wait"
        assert call_kwargs["signal_type"] == "approval"

    def test_wait_signal_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="wait denied"
        )

        with pytest.raises(PolicyError):
            self.ctx.wait_signal("approval")

    def test_wait_signal_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(accepted=True)
        ctx = AgentContext(
            self.mock_client, self.claim,
            signal_events={}, signal_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out"):
            ctx.wait_signal("approval")


class TestBaseAgent:
    def test_handle_execution_completes(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                return {"answer": "42"}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        agent._client.submit_intent.return_value = IntentResult(accepted=True)

        agent._handle_execution(make_claim())

        call_kwargs = agent._client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "complete"
        assert call_kwargs["output"] == {"answer": "42"}

    def test_handle_execution_process_error(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                raise RuntimeError("boom")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        agent._client.submit_intent.return_value = IntentResult(accepted=True)

        agent._handle_execution(make_claim())

        call_kwargs = agent._client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "fail"
        assert "boom" in call_kwargs["error"]


class TestGetTools:
    def setup_method(self):
        self.mock_client = MagicMock(spec=RebunoClient)
        self.claim = make_claim()

    def test_wraps_local_tool(self):
        def search(query: str, limit: int = 10) -> dict:
            """Search the web."""
            return {"results": [query], "limit": limit}

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        tools = ctx.get_tools()
        assert len(tools) == 1

        result = tools[0]("test query")
        assert result == {"results": ["test query"], "limit": 10}

        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["tool_id"] == "web.search"
        assert call_kwargs["arguments"] == {"query": "test query", "limit": 10}

        self.mock_client.report_step_result.assert_called_once()
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is True

    def test_policy_denied(self):
        def search(query: str) -> dict:
            return {}

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="blocked"
        )

        tools = ctx.get_tools()
        with pytest.raises(PolicyError, match="blocked"):
            tools[0]("test")

    def test_preserves_signature(self):
        def search(query: str, limit: int = 10) -> dict:
            """Search the web for results."""
            return {}

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"web.search": search}
        )
        tools = ctx.get_tools()
        wrapper = tools[0]

        assert wrapper.__name__ == "search"
        assert wrapper.__doc__ == "Search the web for results."
        sig = inspect.signature(wrapper)
        assert list(sig.parameters.keys()) == ["query", "limit"]
        assert sig.parameters["limit"].default == 10

    def test_wraps_remote_tool(self):
        def search(query: str) -> dict:
            """Search the web."""
            ...

        result_events = {}
        result_data = {}
        ctx = AgentContext(
            self.mock_client, self.claim,
            remote_tools={"web.search": search},
            result_events=result_events,
            result_data=result_data,
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        _simulate_tool_result(
            result_events, result_data, "step-1",
            {"status": "succeeded", "result": {"data": "found"}},
        )

        tools = ctx.get_tools()
        assert len(tools) == 1

        result = tools[0]("test query")
        assert result == {"data": "found"}

        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["tool_id"] == "web.search"
        assert call_kwargs["arguments"] == {"query": "test query"}
        assert call_kwargs["remote"] is True

    def test_remote_tool_policy_denied(self):
        def search(query: str) -> dict:
            ...

        ctx = AgentContext(
            self.mock_client, self.claim, remote_tools={"web.search": search}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=False, error="blocked"
        )

        tools = ctx.get_tools()
        with pytest.raises(PolicyError, match="blocked"):
            tools[0]("test")

    def test_includes_both_local_and_remote(self):
        def local_search(query: str) -> dict:
            """Local search."""
            return {}

        def remote_fetch(url: str) -> dict:
            """Remote fetch."""
            ...

        ctx = AgentContext(
            self.mock_client,
            self.claim,
            tools={"web.search": local_search},
            remote_tools={"doc.fetch": remote_fetch},
        )
        tools = ctx.get_tools()
        assert len(tools) == 2
        names = {t.__name__ for t in tools}
        assert names == {"local_search", "remote_fetch"}

    def test_local_tool_exception_reports_failure(self):
        def bad_tool(query: str) -> dict:
            raise ValueError("bad input")

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"web.search": bad_tool}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        tools = ctx.get_tools()
        with pytest.raises(ToolError, match="bad input"):
            tools[0]("test")

        self.mock_client.report_step_result.assert_called_once()
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is False
        assert "bad input" in report_kwargs["error"]


class TestBaseAgentInit:
    def test_empty_agent_id_raises(self):
        with pytest.raises(ValueError, match="agent_id"):
            class A(BaseAgent):
                def process(self, ctx):
                    return {}
            A(agent_id="", kernel_url="http://localhost:8080")

    def test_empty_kernel_url_raises(self):
        with pytest.raises(ValueError, match="kernel_url"):
            class A(BaseAgent):
                def process(self, ctx):
                    return {}
            A(agent_id="agent-1", kernel_url="")

    def test_tool_decorator_registers(self):
        class A(BaseAgent):
            def process(self, ctx):
                return {}

        agent = A(agent_id="agent-1", kernel_url="http://localhost:8080")

        @agent.tool("my.tool")
        def my_tool(x: int) -> int:
            return x + 1

        assert "my.tool" in agent._tools
        assert agent._tools["my.tool"] is my_tool

    def test_remote_tool_decorator_registers(self):
        class A(BaseAgent):
            def process(self, ctx):
                return {}

        agent = A(agent_id="agent-1", kernel_url="http://localhost:8080")

        @agent.remote_tool("ext.tool")
        def ext_tool(url: str) -> dict:
            ...

        assert "ext.tool" in agent._remote_tools
        assert agent._remote_tools["ext.tool"] is ext_tool


class TestBaseAgentExecution:
    def test_handle_execution_cleans_up_exec_storage(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                return {"ok": True}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        agent._client.submit_intent.return_value = IntentResult(accepted=True)

        claim = make_claim()
        agent._handle_execution(claim)

        # After execution completes, storage dicts should be cleaned up
        assert claim.execution_id not in agent._exec_result_events
        assert claim.execution_id not in agent._exec_result_data
        assert claim.execution_id not in agent._exec_signal_events
        assert claim.execution_id not in agent._exec_signal_data

    def test_handle_execution_cleans_up_on_error(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                raise RuntimeError("crash")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        agent._client.submit_intent.return_value = IntentResult(accepted=True)

        claim = make_claim()
        agent._handle_execution(claim)

        # Storage should still be cleaned up via finally
        assert claim.execution_id not in agent._exec_result_events

    def test_try_fail_suppresses_exception(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                raise RuntimeError("crash")

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        # First call for the fail intent raises
        agent._client.submit_intent.side_effect = Exception("network down")

        claim = make_claim()
        # Should not raise even though _try_fail itself fails
        agent._handle_execution(claim)


class TestHandleEvent:
    def test_tool_result_dispatched(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                return {}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)

        # Set up exec storage as if an execution is running
        eid = "exec-1"
        result_events = {}
        result_data = {}
        evt = threading.Event()
        result_events["step-1"] = evt
        with agent._exec_lock:
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
        agent._handle_event(event)

        assert evt.is_set()
        assert result_data["step-1"]["status"] == "succeeded"

    def test_signal_received_dispatched(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                return {}

        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)

        eid = "exec-1"
        signal_events = {}
        signal_data = {}
        evt = threading.Event()
        signal_events["approval"] = evt
        with agent._exec_lock:
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
        agent._handle_event(event)

        assert evt.is_set()
        assert signal_data["approval"] == {"approved": True}


class TestHandleEventEdgeCases:
    def _make_agent(self):
        class TestAgent(BaseAgent):
            def process(self, ctx):
                return {}
        agent = TestAgent(agent_id="agent-1", kernel_url="http://localhost:8080")
        agent._client = MagicMock(spec=RebunoClient)
        return agent

    def test_tool_result_unknown_execution_id_ignored(self):
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
        agent._handle_event(event)

    def test_signal_received_unknown_execution_id_ignored(self):
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
        agent._handle_event(event)

    def test_approval_resolved_unknown_execution_id_ignored(self):
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
        agent._handle_event(event)

    def test_execution_assigned_malformed_json_raises(self):
        """When an execution.assigned SSE event has invalid JSON data,
        json.loads raises a JSONDecodeError. The current implementation does
        not catch this, so it propagates up to the SSE processing loop."""
        agent = self._make_agent()

        event = SSEEvent(
            type="execution.assigned",
            data="not valid json {{{",
        )
        with pytest.raises(json.JSONDecodeError):
            agent._handle_event(event)


class TestInvokeToolEdgeCases:
    def setup_method(self):
        self.mock_client = MagicMock(spec=RebunoClient)
        self.claim = make_claim()

    def test_invoke_local_tool_directly(self):
        def add(a: int, b: int) -> int:
            return a + b

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"math.add": add}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        result = ctx.invoke_tool("math.add", {"a": 1, "b": 2})
        assert result == 3

        # Verify report_step_result called with success
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is True
        assert report_kwargs["data"] == 3

    def test_invoke_local_tool_with_non_dict_arguments(self):
        def noop() -> dict:
            return {"ok": True}

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"noop": noop}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        # Non-dict arguments should be ignored with a warning
        result = ctx.invoke_tool("noop", "not-a-dict")
        assert result == {"ok": True}

    def test_invoke_local_tool_error_raises_tool_error(self):
        def fail_tool() -> dict:
            raise RuntimeError("broken")

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"bad.tool": fail_tool}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        with pytest.raises(ToolError, match="broken") as exc_info:
            ctx.invoke_tool("bad.tool")
        assert exc_info.value.tool_id == "bad.tool"
        assert exc_info.value.step_id == "step-1"

    def test_invoke_local_tool_raises_tool_error_directly(self):
        """When a local tool raises a ToolError directly, it should be
        re-raised as-is without being double-wrapped in another ToolError."""
        original_error = ToolError(
            message="custom tool error",
            tool_id="custom.tool",
            step_id="original-step",
            retryable=True,
        )

        def tool_that_raises_tool_error() -> dict:
            raise original_error

        ctx = AgentContext(
            self.mock_client, self.claim,
            tools={"my.tool": tool_that_raises_tool_error},
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )
        self.mock_client.report_step_result.return_value = None

        with pytest.raises(ToolError) as exc_info:
            ctx.invoke_tool("my.tool")

        # The raised error should be the original ToolError, not a new one
        assert exc_info.value is original_error
        assert exc_info.value.tool_id == "custom.tool"
        assert exc_info.value.step_id == "original-step"
        assert exc_info.value.retryable is True

        # Should still report failure to the kernel
        report_kwargs = self.mock_client.report_step_result.call_args.kwargs
        assert report_kwargs["success"] is False

    def test_pending_approval_denied(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1", pending_approval=True,
        )
        approval_events = {}
        approval_data = {}
        ctx = AgentContext(
            self.mock_client, self.claim,
            approval_events=approval_events,
            approval_data=approval_data,
            wait_timeout=2.0,
        )

        def _push_approval():
            deadline = time.monotonic() + 5.0
            while "step-1" not in approval_events:
                if time.monotonic() > deadline:
                    return
                time.sleep(0.01)
            time.sleep(0.05)
            approval_data["step-1"] = {"approved": False}
            evt = approval_events.get("step-1")
            if evt:
                evt.set()

        threading.Thread(target=_push_approval, daemon=True).start()

        with pytest.raises(PolicyError, match="denied by human approval"):
            ctx.invoke_tool("web.search")

    def test_pending_approval_timeout(self):
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1", pending_approval=True,
        )
        ctx = AgentContext(
            self.mock_client, self.claim,
            approval_events={}, approval_data={},
            wait_timeout=0.0,
        )
        with pytest.raises(RebunoError, match="Timed out waiting for approval"):
            ctx.invoke_tool("web.search")


class TestSubmitAndAwaitLocal:
    def setup_method(self):
        self.mock_client = MagicMock(spec=RebunoClient)
        self.mock_client.report_step_result.return_value = None
        self.claim = make_claim()

    def test_submit_local_then_await(self):
        def add(a: int, b: int) -> int:
            return a + b

        ctx = AgentContext(
            self.mock_client, self.claim, tools={"math.add": add}
        )
        self.mock_client.submit_intent.return_value = IntentResult(
            accepted=True, step_id="step-1"
        )

        step_id = ctx.submit_tool("math.add", {"a": 3, "b": 4})
        assert step_id == "step-1"

        results = ctx.await_steps([step_id])
        assert results == [7]

    def test_await_steps_mixed_local_and_remote(self):
        def add(a: int, b: int) -> int:
            return a + b

        result_events = {}
        result_data = {}
        ctx = AgentContext(
            self.mock_client, self.claim,
            tools={"math.add": add},
            result_events=result_events,
            result_data=result_data,
            wait_timeout=2.0,
        )
        self.mock_client.submit_intent.side_effect = [
            IntentResult(accepted=True, step_id="step-local"),
            IntentResult(accepted=True, step_id="step-remote"),
        ]

        local_step = ctx.submit_tool("math.add", {"a": 1, "b": 2})
        remote_step = ctx.submit_tool("web.search", {"q": "test"})

        _simulate_tool_result(
            result_events, result_data, "step-remote",
            {"status": "succeeded", "result": "found"},
        )

        results = ctx.await_steps([local_step, remote_step])
        assert results == [3, "found"]


class TestContextCompleteAndFail:
    def setup_method(self):
        self.mock_client = MagicMock(spec=RebunoClient)
        self.claim = make_claim()

    def test_complete_submits_intent(self):
        ctx = AgentContext(self.mock_client, self.claim)
        ctx.complete({"answer": 42})
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "complete"
        assert call_kwargs["output"] == {"answer": 42}

    def test_fail_submits_intent(self):
        ctx = AgentContext(self.mock_client, self.claim)
        ctx.fail("something went wrong")
        call_kwargs = self.mock_client.submit_intent.call_args.kwargs
        assert call_kwargs["intent_type"] == "fail"
        assert call_kwargs["error"] == "something went wrong"

