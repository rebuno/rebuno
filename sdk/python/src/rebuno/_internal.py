"""Internal utilities, not part of the public API."""
from __future__ import annotations

import random
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass

import httpx

from rebuno.errors import (
    APIError,
    ConflictError,
    NotFoundError,
    UnauthorizedError,
    ValidationError,
)


@dataclass
class SSEEvent:
    type: str
    data: str
    id: str = ""


def api_error(resp: httpx.Response) -> APIError:
    """Construct the appropriate APIError subclass from an HTTP response."""
    try:
        body = resp.json()
    except Exception:
        body = {}

    message = body.get("error") if body else None
    if not message:
        try:
            message = resp.text or "Unknown error"
        except Exception:
            message = "Unknown error"

    code = body.get("code", "UNKNOWN") if body else "UNKNOWN"
    details = body.get("details") if body else None

    cls = _STATUS_TO_ERROR.get(resp.status_code, APIError)
    return cls(
        message=message,
        code=code,
        status_code=resp.status_code,
        details=details,
    )


def jittered_backoff(base_delay: float, attempt: int, max_delay: float) -> float:
    """Compute a reconnect delay with exponential backoff and jitter.

    Args:
        base_delay: Initial delay in seconds.
        attempt: Number of consecutive failures (1-indexed).
        max_delay: Maximum delay cap in seconds.

    Returns:
        Jittered delay in seconds.
    """
    delay = min(base_delay * (2 ** (attempt - 1)), max_delay)
    return delay * (0.5 + random.random() * 0.5)


_STATUS_TO_ERROR: dict[int, type[APIError]] = {
    400: ValidationError,
    401: UnauthorizedError,
    404: NotFoundError,
    409: ConflictError,
}


class _SSEAccumulator:
    """Accumulates SSE fields line-by-line and emits complete events."""

    def __init__(self) -> None:
        self.event_type = ""
        self.event_id = ""
        self.data_lines: list[str] = []

    def feed(self, raw_line: str) -> SSEEvent | None:
        """Process a single line. Returns an SSEEvent on blank-line boundaries, or None."""
        line = raw_line.rstrip("\n\r")
        if line.startswith("event:"):
            self.event_type = line[6:].strip()
        elif line.startswith("data:"):
            self.data_lines.append(line[5:].removeprefix(" "))
        elif line.startswith("id:"):
            self.event_id = line[3:].strip()
        elif line.startswith(("retry:", ":")):
            pass
        elif line == "":
            return self._flush()
        return None

    def _flush(self) -> SSEEvent | None:
        if self.event_type and self.data_lines:
            event = SSEEvent(
                type=self.event_type,
                data="\n".join(self.data_lines),
                id=self.event_id,
            )
        else:
            event = None
        self.event_type = ""
        self.event_id = ""
        self.data_lines = []
        return event


def parse_sse(lines: Iterator[str]) -> Iterator[SSEEvent]:
    acc = _SSEAccumulator()
    for line in lines:
        event = acc.feed(line)
        if event is not None:
            yield event
    trailing = acc._flush()
    if trailing is not None:
        yield trailing


async def async_parse_sse(lines: AsyncIterator[str]) -> AsyncIterator[SSEEvent]:
    acc = _SSEAccumulator()
    async for line in lines:
        event = acc.feed(line)
        if event is not None:
            yield event
    trailing = acc._flush()
    if trailing is not None:
        yield trailing
