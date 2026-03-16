from unittest.mock import patch

import httpx
import pytest

from rebuno._internal import SSEEvent, api_error, parse_sse
from rebuno.client import RebunoClient
from rebuno.errors import (
    APIError,
    ConflictError,
    NetworkError,
    NotFoundError,
    PolicyError,
    RebunoError,
    UnauthorizedError,
    ValidationError,
)
from rebuno.models import ExecutionStatus

from conftest import SAMPLE_EXECUTION, make_response


class TestClientInit:
    def test_base_url_trailing_slash(self):
        client = RebunoClient(base_url="http://localhost:8080/")
        assert client.base_url == "http://localhost:8080"
        client.close()

    def test_api_key_sets_auth_header(self):
        client = RebunoClient(base_url="http://localhost:8080", api_key="test-key")
        assert client._client.headers["authorization"] == "Bearer test-key"
        client.close()


class TestRetryDelay:
    def test_exponential_backoff(self):
        client = RebunoClient(
            base_url="http://localhost:8080",
            retry_base_delay=1.0,
            retry_max_delay=10.0,
        )
        assert client._retry_delay(0) == 1.0
        assert client._retry_delay(1) == 2.0
        assert client._retry_delay(2) == 4.0
        assert client._retry_delay(3) == 8.0
        client.close()

    def test_capped_at_max_delay(self):
        client = RebunoClient(
            base_url="http://localhost:8080",
            retry_base_delay=1.0,
            retry_max_delay=5.0,
        )
        assert client._retry_delay(10) == 5.0
        client.close()


class TestRequest:
    def setup_method(self):
        self.client = RebunoClient(
            base_url="http://localhost:8080",
            max_retries=2,
            retry_base_delay=0.001,
            retry_max_delay=0.01,
        )

    def teardown_method(self):
        self.client.close()

    def test_successful_request(self):
        resp = make_response(200, {"status": "ok"})
        with patch.object(self.client._client, "request", return_value=resp):
            result = self.client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    def test_4xx_raises_api_error(self):
        resp = make_response(404, {"error": "not found", "code": "NOT_FOUND"})
        with patch.object(self.client._client, "request", return_value=resp):
            with pytest.raises(APIError) as exc_info:
                self.client._request("GET", "/v0/executions/missing")
            assert exc_info.value.status_code == 404
            assert exc_info.value.code == "NOT_FOUND"

    def test_403_raises_policy_error(self):
        resp = make_response(403, {"error": "denied by policy", "rule_id": "r-1"})
        with patch.object(self.client._client, "request", return_value=resp):
            with pytest.raises(PolicyError) as exc_info:
                self.client._request("POST", "/v0/agents/intent")
            assert exc_info.value.rule_id == "r-1"
            assert exc_info.value.status_code == 403

    def test_403_empty_body(self):
        resp = make_response(403, None, text="")
        with patch.object(self.client._client, "request", return_value=resp):
            with pytest.raises(PolicyError) as exc_info:
                self.client._request("POST", "/v0/agents/intent")
            assert exc_info.value.rule_id == ""

    def test_5xx_retries_on_get(self):
        error_resp = make_response(503, {"error": "unavailable", "code": "UNAVAILABLE"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request", side_effect=[error_resp, ok_resp]
        ):
            result = self.client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    def test_5xx_no_retry_on_post(self):
        resp = make_response(500, {"error": "internal", "code": "INTERNAL"})
        with patch.object(self.client._client, "request", return_value=resp):
            with pytest.raises(APIError) as exc_info:
                self.client._request("POST", "/v0/executions")
            assert exc_info.value.status_code == 500

    def test_5xx_retries_on_idempotent_post(self):
        error_resp = make_response(503, {"error": "unavailable", "code": "UNAVAILABLE"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request", side_effect=[error_resp, ok_resp]
        ):
            result = self.client._request("POST", "/v0/cancel", idempotent=True)
            assert result.json() == {"status": "ok"}

    def test_5xx_exhausts_retries(self):
        error_resp = make_response(500, {"error": "down", "code": "INTERNAL"})
        with patch.object(
            self.client._client, "request", return_value=error_resp
        ) as mock:
            with pytest.raises(APIError):
                self.client._request("GET", "/v0/health")
            # max_retries=2, so initial + 2 retries = 3 calls
            assert mock.call_count == 3

    def test_429_retries_with_retry_after(self):
        rate_resp = make_response(429, {}, headers={"Retry-After": "0"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request", side_effect=[rate_resp, ok_resp]
        ):
            result = self.client._request("POST", "/v0/executions")
            assert result.json() == {"status": "ok"}

    def test_429_invalid_retry_after_defaults_to_1(self):
        rate_resp = make_response(429, {}, headers={"Retry-After": "not-a-number"})
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request", side_effect=[rate_resp, ok_resp]
        ) as mock, patch("rebuno.client.time.sleep") as sleep_mock:
            result = self.client._request("POST", "/v0/executions")
            assert result.json() == {"status": "ok"}
            sleep_mock.assert_called_with(1.0)

    def test_429_exhausts_retries(self):
        rate_resp = make_response(429, {"error": "rate limited"}, headers={"Retry-After": "0"})
        with patch.object(
            self.client._client, "request", return_value=rate_resp
        ):
            with pytest.raises(APIError) as exc_info:
                self.client._request("POST", "/v0/executions")
            assert exc_info.value.status_code == 429

    def test_connection_error_retries_on_get(self):
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request",
            side_effect=[httpx.ConnectError("refused"), ok_resp],
        ):
            result = self.client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    def test_connection_error_no_retry_on_post(self):
        with patch.object(
            self.client._client, "request",
            side_effect=httpx.ConnectError("refused"),
        ):
            with pytest.raises(NetworkError):
                self.client._request("POST", "/v0/executions")

    def test_timeout_error_retries_on_get(self):
        ok_resp = make_response(200, {"status": "ok"})
        with patch.object(
            self.client._client, "request",
            side_effect=[httpx.ReadTimeout("timeout"), ok_resp],
        ):
            result = self.client._request("GET", "/v0/health")
            assert result.json() == {"status": "ok"}

    def test_connection_error_exhausts_retries(self):
        with patch.object(
            self.client._client, "request",
            side_effect=httpx.ConnectError("refused"),
        ) as mock:
            with pytest.raises(NetworkError):
                self.client._request("GET", "/v0/health")
            assert mock.call_count == 3

    def test_unexpected_error_raises_rebuno_error(self):
        with patch.object(
            self.client._client, "request",
            side_effect=RuntimeError("unexpected"),
        ):
            with pytest.raises(RebunoError):
                self.client._request("GET", "/v0/health")

    def test_api_error_not_retried(self):
        """Non-5xx APIError (e.g. 400) should not be retried even on GET."""
        resp = make_response(400, {"error": "bad", "code": "INVALID"})
        with patch.object(
            self.client._client, "request", return_value=resp
        ) as mock:
            with pytest.raises(APIError):
                self.client._request("GET", "/v0/executions")
            assert mock.call_count == 1


class TestExecutionEndpoints:
    def setup_method(self):
        self.client = RebunoClient(base_url="http://localhost:8080")

    def teardown_method(self):
        self.client.close()

    def test_create_execution(self):
        resp = make_response(200, SAMPLE_EXECUTION)
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            result = self.client.create_execution(
                agent_id="agent-1",
                input={"query": "hello"},
                labels={"env": "test"},
            )
            assert result.id == "exec-1"
            assert result.status == ExecutionStatus.RUNNING
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body["agent_id"] == "agent-1"
            assert body["input"] == {"query": "hello"}
            assert body["labels"] == {"env": "test"}

    def test_create_execution_minimal(self):
        resp = make_response(200, SAMPLE_EXECUTION)
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.create_execution(agent_id="agent-1")
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body == {"agent_id": "agent-1"}

    def test_get_execution(self):
        resp = make_response(200, SAMPLE_EXECUTION)
        with patch.object(self.client._client, "request", return_value=resp):
            execution = self.client.get_execution("exec-1")
            assert execution.id == "exec-1"
            assert execution.status == ExecutionStatus.RUNNING

    def test_list_executions_with_all_params(self):
        resp = make_response(200, {"executions": [], "next_cursor": ""})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.list_executions(
                status=ExecutionStatus.RUNNING,
                agent_id="agent-1",
                limit=10,
                cursor="abc",
            )
            call_kwargs = mock.call_args
            params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
            assert params["status"] == "running"
            assert params["agent_id"] == "agent-1"
            assert params["limit"] == 10
            assert params["cursor"] == "abc"

    def test_list_executions_no_filters(self):
        resp = make_response(200, {"executions": [], "next_cursor": ""})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.list_executions()
            call_kwargs = mock.call_args
            params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
            assert params == {"limit": 50}
            assert "status" not in params
            assert "agent_id" not in params

    def test_cancel_execution(self):
        cancelled = {**SAMPLE_EXECUTION, "status": "cancelled"}
        resp = make_response(200, cancelled)
        with patch.object(self.client._client, "request", return_value=resp):
            execution = self.client.cancel_execution("exec-1")
            assert execution.status == ExecutionStatus.CANCELLED

    def test_send_signal(self):
        resp = make_response(200, {"status": "delivered"})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            result = self.client.send_signal("exec-1", "approval", {"approved": True})
            assert result.status == "delivered"
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body["signal_type"] == "approval"
            assert body["payload"] == {"approved": True}

    def test_send_signal_no_payload(self):
        resp = make_response(200, {"status": "delivered"})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.send_signal("exec-1", "cancel")
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert "payload" not in body

    def test_get_events(self):
        resp = make_response(200, {"events": [], "latest_sequence": 0})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            result = self.client.get_events("exec-1", after_sequence=5, limit=50)
            call_kwargs = mock.call_args
            params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
            assert params["after_sequence"] == 5
            assert params["limit"] == 50


class TestAgentEndpoints:
    def setup_method(self):
        self.client = RebunoClient(base_url="http://localhost:8080")

    def teardown_method(self):
        self.client.close()

    def test_submit_intent_body_construction(self):
        """Verify all optional fields are included in the request body when set."""
        resp = make_response(200, {"accepted": True, "step_id": "step-1"})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.submit_intent(
                execution_id="exec-1",
                session_id="sess-1",
                intent_type="invoke_tool",
                tool_id="web.search",
                arguments={"q": "test"},
                idempotency_key="key-1",
                remote=True,
            )
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body["execution_id"] == "exec-1"
            assert body["session_id"] == "sess-1"
            intent = body["intent"]
            assert intent["type"] == "invoke_tool"
            assert intent["tool_id"] == "web.search"
            assert intent["arguments"] == {"q": "test"}
            assert intent["idempotency_key"] == "key-1"
            assert intent["remote"] is True

    def test_submit_intent_minimal(self):
        """Verify optional fields are omitted from the intent when not set."""
        resp = make_response(200, {"accepted": True})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.submit_intent(
                execution_id="exec-1",
                session_id="sess-1",
                intent_type="complete",
            )
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            intent = body["intent"]
            assert intent == {"type": "complete"}

    def test_report_step_result(self):
        resp = make_response(200, {})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.report_step_result(
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


class TestRunnerEndpoints:
    def setup_method(self):
        self.client = RebunoClient(base_url="http://localhost:8080")

    def teardown_method(self):
        self.client.close()

    def test_submit_result(self):
        resp = make_response(200, {"ok": True})
        with patch.object(self.client._client, "request", return_value=resp):
            result = self.client.submit_result(
                runner_id="runner-1",
                job_id="job-1",
                execution_id="exec-1",
                step_id="step-1",
                success=True,
                data={"output": "result"},
            )
            assert result["ok"] is True

    def test_submit_result_with_timestamps(self):
        resp = make_response(200, {"ok": True})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.submit_result(
                runner_id="runner-1",
                job_id="job-1",
                execution_id="exec-1",
                step_id="step-1",
                success=False,
                error="timeout",
                retryable=True,
                started_at="2025-01-01T00:00:00Z",
                completed_at="2025-01-01T00:01:00Z",
            )
            call_kwargs = mock.call_args
            body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
            assert body["error"] == "timeout"
            assert body["retryable"] is True
            assert body["started_at"] == "2025-01-01T00:00:00Z"
            assert body["completed_at"] == "2025-01-01T00:01:00Z"

    def test_step_started(self):
        resp = make_response(200, {})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.step_started("step-1", "exec-1", "runner-1")
            call_kwargs = mock.call_args
            assert call_kwargs[0][1] == "/v0/runners/steps/step-1/started"

    def test_unregister_runner(self):
        resp = make_response(200, {})
        with patch.object(self.client._client, "request", return_value=resp) as mock:
            self.client.unregister_runner("runner-1")
            assert call_kwargs[0][0] == "DELETE" if (call_kwargs := mock.call_args) else True

    def test_health(self):
        resp = make_response(200, {"status": "ok"})
        with patch.object(self.client._client, "request", return_value=resp):
            result = self.client.health()
            assert result["status"] == "ok"

    def test_ready(self):
        resp = make_response(200, {"status": "ready"})
        with patch.object(self.client._client, "request", return_value=resp):
            result = self.client.ready()
            assert result["status"] == "ready"


class TestApiErrorHelper:
    def test_json_body(self):
        resp = make_response(400, {"error": "bad request", "code": "INVALID", "details": {"field": "name"}})
        err = api_error(resp)
        assert isinstance(err, ValidationError)
        assert err.code == "INVALID"
        assert err.status_code == 400
        assert err.details == {"field": "name"}

    def test_non_json_body(self):
        resp = httpx.Response(
            status_code=500,
            content=b"Internal Server Error",
            headers={"content-type": "text/plain"},
            request=httpx.Request("GET", "http://test"),
        )
        err = api_error(resp)
        assert err.code == "UNKNOWN"
        assert err.status_code == 500
        assert "Internal Server Error" in str(err)

    def test_empty_body(self):
        resp = httpx.Response(
            status_code=502,
            content=b"",
            headers={"content-type": "text/plain"},
            request=httpx.Request("GET", "http://test"),
        )
        err = api_error(resp)
        assert err.status_code == 502

    @pytest.mark.parametrize("status,expected_cls", [
        (400, ValidationError),
        (401, UnauthorizedError),
        (404, NotFoundError),
        (409, ConflictError),
        (500, APIError),
        (503, APIError),
    ])
    def test_status_code_to_error_class(self, status, expected_cls):
        resp = make_response(status, {"error": "test", "code": "TEST"})
        err = api_error(resp)
        assert type(err) is expected_cls


class TestClient403NonJsonBody:
    def setup_method(self):
        self.client = RebunoClient(
            base_url="http://localhost:8080",
            max_retries=0,
        )

    def teardown_method(self):
        self.client.close()

    def test_403_html_body_raises_rebuno_error(self):
        """When a 403 response has a non-JSON body (e.g. HTML from a reverse
        proxy), resp.json() raises a JSON decode error. The generic exception
        handler wraps it as a RebunoError instead of a PolicyError. This
        documents the current behavior -- ideally it would be a PolicyError."""
        resp = make_response(403, None, text="<html>Forbidden</html>")
        with patch.object(self.client._client, "request", return_value=resp):
            with pytest.raises(RebunoError):
                self.client._request("POST", "/v0/agents/intent")


class TestParseSSE:
    def test_basic_event(self):
        lines = iter(["event: test\n", "data: hello\n", "\n"])
        events = list(parse_sse(lines))
        assert len(events) == 1
        assert events[0] == SSEEvent(type="test", data="hello")

    def test_multi_line_data(self):
        lines = iter(["event: msg\n", "data: line1\n", "data: line2\n", "\n"])
        events = list(parse_sse(lines))
        assert events[0].data == "line1\nline2"

    def test_empty_data_line(self):
        lines = iter(["event: msg\n", "data:\n", "data: after\n", "\n"])
        events = list(parse_sse(lines))
        assert events[0].data == "\nafter"

    def test_no_space_after_colon(self):
        lines = iter(["event:test\n", "data:hello\n", "\n"])
        events = list(parse_sse(lines))
        assert events[0].type == "test"
        assert events[0].data == "hello"

    def test_comments_skipped(self):
        lines = iter([":heartbeat\n", "event: test\n", ": another comment\n", "data: hi\n", "\n"])
        events = list(parse_sse(lines))
        assert len(events) == 1
        assert events[0].data == "hi"

    def test_missing_event_type_skipped(self):
        lines = iter(["data: orphan\n", "\n"])
        events = list(parse_sse(lines))
        assert len(events) == 0

    def test_missing_data_skipped(self):
        lines = iter(["event: test\n", "\n"])
        events = list(parse_sse(lines))
        assert len(events) == 0

    def test_id_field_parsed(self):
        lines = iter(["event: test\n", "data: hi\n", "id: 42\n", "\n"])
        events = list(parse_sse(lines))
        assert events[0].id == "42"

    def test_multiple_events(self):
        lines = iter([
            "event: a\n", "data: 1\n", "\n",
            "event: b\n", "data: 2\n", "\n",
        ])
        events = list(parse_sse(lines))
        assert len(events) == 2
        assert events[0].type == "a"
        assert events[1].type == "b"

    def test_flush_on_stream_close_without_trailing_blank(self):
        lines = iter(["event: test\n", "data: final\n"])
        events = list(parse_sse(lines))
        assert len(events) == 1
        assert events[0].data == "final"

    def test_only_single_space_stripped(self):
        lines = iter(["event: test\n", "data:  two spaces\n", "\n"])
        events = list(parse_sse(lines))
        assert events[0].data == " two spaces"

    def test_state_resets_between_events(self):
        lines = iter([
            "event: a\n", "data: 1\n", "id: x\n", "\n",
            "event: b\n", "data: 2\n", "\n",
        ])
        events = list(parse_sse(lines))
        assert events[0].id == "x"
        assert events[1].id == ""
