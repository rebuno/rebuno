from unittest.mock import AsyncMock, patch

import httpx
import pytest

from rebuno._internal import SSEEvent, async_parse_sse
from rebuno.async_client import AsyncRebunoClient
from rebuno.errors import APIError, NetworkError, PolicyError, RebunoError
from rebuno.models import ExecutionStatus

from conftest import SAMPLE_EXECUTION, make_response


@pytest.fixture
def client():
    c = AsyncRebunoClient(
        base_url="http://localhost:8080",
        max_retries=2,
        retry_base_delay=0.001,
        retry_max_delay=0.01,
    )
    yield c


class TestAsyncClientInit:
    def test_base_url_trailing_slash(self):
        client = AsyncRebunoClient(base_url="http://localhost:8080/")
        assert client.base_url == "http://localhost:8080"

    def test_api_key_sets_auth_header(self):
        client = AsyncRebunoClient(base_url="http://localhost:8080", api_key="test-key")
        assert client._client.headers["authorization"] == "Bearer test-key"


class TestAsyncRequest:
    @pytest.mark.asyncio
    async def test_successful_request(self, client):
        resp = make_response(200, {"status": "ok"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    @pytest.mark.asyncio
    async def test_4xx_raises_api_error(self, client):
        resp = make_response(404, {"error": "not found", "code": "NOT_FOUND"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            with pytest.raises(APIError) as exc_info:
                await client._request("GET", "/v0/executions/missing")
            assert exc_info.value.status_code == 404

    @pytest.mark.asyncio
    async def test_403_raises_policy_error(self, client):
        resp = make_response(403, {"error": "denied", "rule_id": "r-1"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            with pytest.raises(PolicyError) as exc_info:
                await client._request("POST", "/v0/agents/intent")
            assert exc_info.value.rule_id == "r-1"

    @pytest.mark.asyncio
    async def test_5xx_retries_on_get(self, client):
        error_resp = make_response(503, {"error": "unavailable", "code": "UNAVAILABLE"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=[error_resp, ok_resp],
        ):
            result = await client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    @pytest.mark.asyncio
    async def test_5xx_no_retry_on_post(self, client):
        resp = make_response(500, {"error": "internal", "code": "INTERNAL"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            with pytest.raises(APIError):
                await client._request("POST", "/v0/executions")

    @pytest.mark.asyncio
    async def test_5xx_retries_on_idempotent_post(self, client):
        error_resp = make_response(503, {"error": "unavailable", "code": "UNAVAILABLE"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=[error_resp, ok_resp],
        ):
            result = await client._request("POST", "/v0/cancel", idempotent=True)
            assert result.json() == {"status": "ok"}

    @pytest.mark.asyncio
    async def test_5xx_exhausts_retries(self, client):
        error_resp = make_response(500, {"error": "down", "code": "INTERNAL"})
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            return_value=error_resp,
        ) as mock:
            with pytest.raises(APIError):
                await client._request("GET", "/v0/health")
            assert mock.call_count == 3

    @pytest.mark.asyncio
    async def test_connection_error_retries_on_get(self, client):
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=[httpx.ConnectError("refused"), ok_resp],
        ):
            result = await client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    @pytest.mark.asyncio
    async def test_connection_error_no_retry_on_post(self, client):
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=httpx.ConnectError("refused"),
        ):
            with pytest.raises(NetworkError):
                await client._request("POST", "/v0/executions")

    @pytest.mark.asyncio
    async def test_429_retries(self, client):
        rate_resp = make_response(429, {}, headers={"Retry-After": "0"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=[rate_resp, ok_resp],
        ):
            result = await client._request("POST", "/v0/executions")
            assert result.json() == {"status": "ok"}

    @pytest.mark.asyncio
    async def test_unexpected_error_raises_rebuno_error(self, client):
        with patch.object(
            client._client, "request", new_callable=AsyncMock,
            side_effect=RuntimeError("unexpected"),
        ):
            with pytest.raises(RebunoError):
                await client._request("GET", "/v0/health")


class TestAsyncClient403NonJsonBody:
    @pytest.mark.asyncio
    async def test_403_html_body_raises_rebuno_error(self, client):
        """When a 403 response has a non-JSON body (e.g. HTML from a reverse
        proxy), resp.json() raises a JSON decode error. The generic exception
        handler wraps it as a RebunoError instead of a PolicyError. This
        documents the current behavior -- ideally it would be a PolicyError."""
        resp = make_response(403, None, text="<html>Forbidden</html>")
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            with pytest.raises(RebunoError):
                await client._request("POST", "/v0/agents/intent")


class TestAsyncExecutionEndpoints:
    @pytest.mark.asyncio
    async def test_create_execution(self, client):
        resp = make_response(200, SAMPLE_EXECUTION)
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client.create_execution(agent_id="agent-1", input={"q": "hi"})
            assert result.id == "exec-1"
            assert result.status == ExecutionStatus.RUNNING

    @pytest.mark.asyncio
    async def test_create_execution_minimal(self, client):
        resp = make_response(200, SAMPLE_EXECUTION)
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.create_execution(agent_id="agent-1")
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body == {"agent_id": "agent-1"}

    @pytest.mark.asyncio
    async def test_list_executions_with_filters(self, client):
        resp = make_response(200, {"executions": [], "next_cursor": ""})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.list_executions(
                status=ExecutionStatus.RUNNING, agent_id="agent-1",
                limit=10, cursor="abc",
            )
            call_kwargs = mock.call_args
            params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
            assert params["status"] == "running"
            assert params["agent_id"] == "agent-1"

    @pytest.mark.asyncio
    async def test_list_executions_no_filters(self, client):
        resp = make_response(200, {"executions": [], "next_cursor": ""})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.list_executions()
            call_kwargs = mock.call_args
            params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
            assert params == {"limit": 50}

    @pytest.mark.asyncio
    async def test_cancel_execution(self, client):
        cancelled = {**SAMPLE_EXECUTION, "status": "cancelled"}
        resp = make_response(200, cancelled)
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            execution = await client.cancel_execution("exec-1")
            assert execution.status == ExecutionStatus.CANCELLED

    @pytest.mark.asyncio
    async def test_send_signal(self, client):
        resp = make_response(200, {"status": "delivered"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client.send_signal("exec-1", "approval", {"ok": True})
            assert result.status == "delivered"

    @pytest.mark.asyncio
    async def test_get_events(self, client):
        resp = make_response(200, {"events": [], "latest_sequence": 5})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client.get_events("exec-1", after_sequence=3)
            assert result.latest_sequence == 5


class TestAsyncAgentEndpoints:
    @pytest.mark.asyncio
    async def test_submit_intent_body_construction(self, client):
        resp = make_response(200, {"accepted": True, "step_id": "step-1"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.submit_intent(
                execution_id="exec-1",
                session_id="sess-1",
                intent_type="invoke_tool",
                tool_id="web.search",
                arguments={"q": "test"},
                remote=True,
            )
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            intent = body["intent"]
            assert intent["tool_id"] == "web.search"
            assert intent["remote"] is True

    @pytest.mark.asyncio
    async def test_report_step_result(self, client):
        resp = make_response(200, {})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.report_step_result(
                execution_id="exec-1",
                session_id="sess-1",
                step_id="step-1",
                success=True,
                data={"result": "ok"},
            )
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body["step_id"] == "step-1"
            assert body["success"] is True


class TestAsyncRunnerEndpoints:
    @pytest.mark.asyncio
    async def test_submit_result(self, client):
        resp = make_response(200, {"ok": True})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client.submit_result(
                runner_id="runner-1",
                job_id="job-1",
                execution_id="exec-1",
                step_id="step-1",
                success=True,
            )
            assert result["ok"] is True

    @pytest.mark.asyncio
    async def test_step_started(self, client):
        resp = make_response(200, {})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.step_started("step-1", "exec-1", "runner-1")
            call_kwargs = mock.call_args
            assert call_kwargs[0][1] == "/v0/runners/steps/step-1/started"

    @pytest.mark.asyncio
    async def test_unregister_runner(self, client):
        resp = make_response(200, {})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.unregister_runner("runner-1")
            assert mock.call_args[0][0] == "DELETE"

    @pytest.mark.asyncio
    async def test_health(self, client):
        resp = make_response(200, {"status": "ok"})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp):
            result = await client.health()
            assert result["status"] == "ok"

    @pytest.mark.asyncio
    async def test_update_capabilities(self, client):
        resp = make_response(200, {})
        with patch.object(client._client, "request", new_callable=AsyncMock, return_value=resp) as mock:
            await client.update_capabilities("runner-1", ["tool.a", "tool.b"])
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body == {"tools": ["tool.a", "tool.b"]}


async def _aiter(lines):
    for line in lines:
        yield line


class TestAsyncParseSSE:
    @pytest.mark.asyncio
    async def test_basic_event(self):
        events = [e async for e in async_parse_sse(_aiter(["event: test\n", "data: hello\n", "\n"]))]
        assert len(events) == 1
        assert events[0] == SSEEvent(type="test", data="hello")

    @pytest.mark.asyncio
    async def test_flush_on_stream_close_without_trailing_blank(self):
        events = [e async for e in async_parse_sse(_aiter(["event: test\n", "data: final\n"]))]
        assert len(events) == 1
        assert events[0].data == "final"
