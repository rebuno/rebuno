from rebuno.errors import APIError, PolicyError, RebunoError, ToolError


class TestRebunoError:
    def test_message_and_defaults(self):
        e = RebunoError("something went wrong")
        assert str(e) == "something went wrong"
        assert e.details == {}


class TestAPIError:
    def test_attributes(self):
        e = APIError(
            message="not found", code="NOT_FOUND", status_code=404,
            details={"id": "exec-1"},
        )
        assert e.code == "NOT_FOUND"
        assert e.status_code == 404
        assert e.details == {"id": "exec-1"}

    def test_str_format(self):
        e = APIError(message="bad request", code="INVALID", status_code=400)
        assert str(e) == "[INVALID] bad request (HTTP 400)"


class TestPolicyError:
    def test_attributes(self):
        e = PolicyError("denied by policy", rule_id="rule-42")
        assert e.rule_id == "rule-42"
        assert e.code == "policy_denied"
        assert e.status_code == 403


class TestToolError:
    def test_attributes(self):
        e = ToolError(
            message="execution failed", tool_id="web.search",
            step_id="step-1", retryable=True,
        )
        assert e.tool_id == "web.search"
        assert e.step_id == "step-1"
        assert e.retryable is True
