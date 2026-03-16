import json
from unittest.mock import MagicMock

import pytest

from rebuno._internal import SSEEvent
from rebuno.client import RebunoClient
from rebuno.runner import BaseRunner

from conftest import make_job


def _make_runner(**kwargs):
    """Helper to create a concrete BaseRunner subclass instance."""

    class TestRunner(BaseRunner):
        def execute(self, tool_id, arguments):
            return {"result": "ok"}

    defaults = {"runner_id": "runner-1", "kernel_url": "http://localhost:8080"}
    defaults.update(kwargs)
    return TestRunner(**defaults)


class TestBaseRunnerInit:
    def test_defaults(self):
        runner = _make_runner(capabilities=["web.search"])
        assert runner.runner_id == "runner-1"
        assert runner.name == "runner-1"  # defaults to runner_id
        assert runner.capabilities == ["web.search"]
        assert runner.consumer_id.startswith("runner-1-")
        assert len(runner.consumer_id) == len("runner-1-") + 8
        assert runner._running is False
        runner._client.close()

    def test_empty_runner_id_raises(self):
        with pytest.raises(ValueError, match="runner_id must not be empty"):
            _make_runner(runner_id="")

    def test_empty_kernel_url_raises(self):
        with pytest.raises(ValueError, match="kernel_url must not be empty"):
            _make_runner(kernel_url="")


class TestBaseRunnerHandleJob:
    def test_success(self):
        runner = _make_runner()
        runner._client = MagicMock(spec=RebunoClient)

        job = make_job()
        runner._handle_job(job)

        runner._client.step_started.assert_called_once_with(
            step_id="step-1",
            execution_id="exec-1",
            runner_id="runner-1",
        )
        runner._client.submit_result.assert_called_once()
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True
        assert call_kwargs["data"] == {"result": "ok"}

    def test_execute_error_submits_failure(self):
        class FailRunner(BaseRunner):
            def execute(self, tool_id, arguments):
                raise ValueError("unknown tool")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = MagicMock(spec=RebunoClient)

        runner._handle_job(make_job())

        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is False
        assert "unknown tool" in call_kwargs["error"]
        assert call_kwargs["retryable"] is False

    def test_retryable_error_sets_retryable_flag(self):
        class RetryableError(Exception):
            retryable = True

        class FailRunner(BaseRunner):
            def execute(self, tool_id, arguments):
                raise RetryableError("temporary failure")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = MagicMock(spec=RebunoClient)

        runner._handle_job(make_job())

        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["retryable"] is True

    def test_step_started_failure_does_not_prevent_execution(self):
        runner = _make_runner()
        runner._client = MagicMock(spec=RebunoClient)
        runner._client.step_started.side_effect = Exception("network error")

        runner._handle_job(make_job())

        runner._client.submit_result.assert_called_once()
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True

    def test_submit_result_failure_on_success_path_is_swallowed(self):
        runner = _make_runner()
        runner._client = MagicMock(spec=RebunoClient)
        runner._client.submit_result.side_effect = Exception("network down")

        # Should not raise
        runner._handle_job(make_job())

    def test_submit_result_failure_on_error_path_is_swallowed(self):
        class FailRunner(BaseRunner):
            def execute(self, tool_id, arguments):
                raise ValueError("tool failed")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = MagicMock(spec=RebunoClient)
        runner._client.submit_result.side_effect = Exception("network down")

        # Should not raise even when both execute() and submit_result() fail
        runner._handle_job(make_job())


class TestBaseRunnerConnectAndProcess:
    def test_dispatches_job_assigned_events(self):
        runner = _make_runner(capabilities=["web.search"])
        runner._client = MagicMock(spec=RebunoClient)
        runner._running = True

        job_data = json.dumps(make_job().model_dump())
        events = [SSEEvent(type="job.assigned", data=job_data)]
        runner._client.runner_stream.return_value = iter(events)

        runner._connect_and_process()

        runner._client.submit_result.assert_called_once()

    def test_ignores_non_job_events(self):
        runner = _make_runner()
        runner._client = MagicMock(spec=RebunoClient)
        runner._running = True

        events = [
            SSEEvent(type="heartbeat", data="{}"),
            SSEEvent(type="runner.registered", data="{}"),
        ]
        runner._client.runner_stream.return_value = iter(events)

        runner._connect_and_process()

        runner._client.submit_result.assert_not_called()

    def test_stops_when_running_is_false(self):
        runner = _make_runner()
        runner._client = MagicMock(spec=RebunoClient)
        runner._running = False

        job_data = json.dumps(make_job().model_dump())
        events = [SSEEvent(type="job.assigned", data=job_data)]
        runner._client.runner_stream.return_value = iter(events)

        runner._connect_and_process()

        runner._client.submit_result.assert_not_called()
