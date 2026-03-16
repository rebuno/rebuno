from datetime import datetime, timezone

from rebuno.models import (
    ClaimResult,
    EventList,
    Execution,
    ExecutionStatus,
    ExecutionSummary,
    HistoryEntry,
    ListExecutionsResult,
    StepStatus,
)


class TestExecution:
    def test_from_dict_with_string_status(self):
        data = {
            "id": "exec-1",
            "status": "running",
            "agent_id": "agent-1",
            "labels": {"env": "test"},
        }
        e = Execution(**data)
        assert e.status == ExecutionStatus.RUNNING
        assert e.labels["env"] == "test"

    def test_labels_null_coerced_to_empty_dict(self):
        e = Execution(id="exec-1", status="pending", agent_id="a-1", labels=None)
        assert e.labels == {}


class TestClaimResult:
    def test_full(self):
        c = ClaimResult(
            execution_id="exec-1",
            session_id="sess-1",
            agent_id="agent-1",
            input={"query": "test"},
            labels={"env": "prod"},
            history=[
                HistoryEntry(
                    step_id="step-0", tool_id="web.search",
                    status=StepStatus.SUCCEEDED,
                )
            ],
        )
        assert len(c.history) == 1
        assert c.labels["env"] == "prod"


class TestEventList:
    def test_with_events(self):
        el = EventList(
            events=[
                {"id": "e1", "execution_id": "x1", "type": "a"},
                {"id": "e2", "execution_id": "x1", "type": "b"},
            ],
            latest_sequence=2,
        )
        assert len(el.events) == 2
        assert el.events[0].id == "e1"


class TestListExecutionsResult:
    def test_with_cursor(self):
        now = datetime.now(timezone.utc)
        r = ListExecutionsResult(
            executions=[{
                "id": "exec-1", "status": "running", "agent_id": "a",
                "created_at": now.isoformat(), "updated_at": now.isoformat(),
            }],
            next_cursor="abc123",
        )
        assert len(r.executions) == 1
        assert r.next_cursor == "abc123"
        assert isinstance(r.executions[0], ExecutionSummary)
