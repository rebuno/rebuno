from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Any

from pydantic import BaseModel, Field, field_validator


class ExecutionStatus(str, Enum):
    PENDING = "pending"
    RUNNING = "running"
    BLOCKED = "blocked"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


class StepStatus(str, Enum):
    PENDING = "pending"
    DISPATCHED = "dispatched"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    TIMED_OUT = "timed_out"
    CANCELLED = "cancelled"


class Execution(BaseModel):
    id: str
    status: ExecutionStatus
    agent_id: str
    labels: dict[str, str] = Field(default_factory=dict)
    input: Any = None
    output: Any = None
    created_at: datetime | None = None
    updated_at: datetime | None = None

    @field_validator("labels", mode="before")
    @classmethod
    def _coerce_labels(cls, v: Any) -> dict[str, str]:
        return v if v is not None else {}


class Event(BaseModel):
    id: str
    execution_id: str
    step_id: str = ""
    type: str
    schema_version: int = 1
    timestamp: datetime | None = None
    payload: Any = None
    sequence: int = 0
    idempotency_key: str = ""
    causation_id: str = ""
    correlation_id: str = ""


class Step(BaseModel):
    id: str
    execution_id: str
    tool_id: str
    tool_version: int = 1
    status: StepStatus = StepStatus.PENDING
    attempt: int = 1
    max_attempts: int = 3
    arguments: Any = None
    result: Any = None
    error: str = ""
    retryable: bool = False
    idempotency_key: str = ""
    deadline: datetime | None = None
    runner_id: str = ""
    created_at: datetime | None = None
    dispatched_at: datetime | None = None
    started_at: datetime | None = None
    completed_at: datetime | None = None


class Intent(BaseModel):
    type: str
    tool_id: str = ""
    arguments: Any = None
    idempotency_key: str = ""
    signal_type: str = ""
    output: Any = None
    error: str = ""
    remote: bool = False


class IntentResult(BaseModel):
    accepted: bool
    step_id: str = ""
    error: str = ""
    pending_approval: bool = False


class Job(BaseModel):
    id: str
    execution_id: str
    step_id: str
    attempt: int = 1
    tool_id: str
    tool_version: int = 1
    arguments: Any = None
    deadline: datetime | None = None


class JobResult(BaseModel):
    job_id: str
    execution_id: str
    step_id: str
    success: bool
    data: Any = None
    error: str = ""
    retryable: bool = False
    started_at: datetime | None = None
    completed_at: datetime | None = None
    runner_id: str = ""


class Signal(BaseModel):
    id: str = ""
    execution_id: str = ""
    signal_type: str
    payload: Any = None
    created_at: str = ""


class ToolSummary(BaseModel):
    id: str
    version: int = 1
    name: str
    description: str = ""


class HistoryEntry(BaseModel):
    step_id: str
    tool_id: str
    status: StepStatus
    arguments: Any = None
    result: Any = None
    error: str = ""
    completed_at: datetime | None = None


class ClaimResult(BaseModel):
    execution_id: str
    session_id: str
    agent_id: str
    input: Any = None
    labels: dict[str, str] = Field(default_factory=dict)
    history: list[HistoryEntry] = Field(default_factory=list)


class ExecutionSummary(BaseModel):
    id: str
    status: ExecutionStatus
    agent_id: str
    created_at: datetime
    updated_at: datetime


class ListExecutionsResult(BaseModel):
    executions: list[ExecutionSummary]
    next_cursor: str = ""


class EventList(BaseModel):
    events: list[Event]
    latest_sequence: int


class SignalResult(BaseModel):
    status: str
