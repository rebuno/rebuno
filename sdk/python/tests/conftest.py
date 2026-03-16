import json
from typing import Any

import httpx

from rebuno.models import ClaimResult, Job


def make_response(
    status_code: int = 200,
    json_data: Any = None,
    text: str = "",
    headers: dict[str, str] | None = None,
) -> httpx.Response:
    if json_data is not None:
        content = json.dumps(json_data).encode()
        default_headers = {"content-type": "application/json"}
    else:
        content = text.encode()
        default_headers = {}

    if headers:
        default_headers.update(headers)

    return httpx.Response(
        status_code=status_code,
        content=content,
        headers=default_headers,
        request=httpx.Request("GET", "http://test"),
    )


SAMPLE_EXECUTION = {
    "id": "exec-1",
    "status": "running",
    "agent_id": "agent-1",
    "labels": {"env": "test"},
}

SAMPLE_CLAIM = {
    "execution_id": "exec-1",
    "session_id": "sess-1",
    "agent_id": "agent-1",
    "input": {"query": "hello"},
    "labels": {"env": "test"},
    "history": [],
}

SAMPLE_JOB = {
    "id": "job-1",
    "execution_id": "exec-1",
    "step_id": "step-1",
    "tool_id": "web.search",
    "tool_version": 1,
    "attempt": 1,
    "arguments": {"query": "test"},
}

def make_claim(**overrides) -> ClaimResult:
    defaults = {
        "execution_id": "exec-1",
        "session_id": "sess-1",
        "agent_id": "agent-1",
        "input": {"query": "hello"},
        "labels": {},
        "history": [],
    }
    defaults.update(overrides)
    return ClaimResult(**defaults)


def make_job(**overrides) -> Job:
    defaults = {
        "id": "job-1",
        "execution_id": "exec-1",
        "step_id": "step-1",
        "tool_id": "web.search",
        "arguments": {"q": "test"},
    }
    defaults.update(overrides)
    return Job(**defaults)
