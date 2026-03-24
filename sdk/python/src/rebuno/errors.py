from __future__ import annotations
from typing import Any


class RebunoError(Exception):
    """Base exception for all Rebuno SDK errors."""

    def __init__(self, message: str, details: dict[str, Any] | None = None):
        super().__init__(message)
        self.details = details or {}


class NetworkError(RebunoError):
    """Raised when a network-level error occurs (connection refused, timeout, etc.)."""

    def __repr__(self) -> str:
        return f"NetworkError({str(self)!r})"


class APIError(RebunoError):
    """Raised when the API returns an error response."""

    def __init__(
        self,
        message: str,
        code: str,
        status_code: int,
        details: dict[str, Any] | None = None,
    ):
        super().__init__(message, details)
        self.code = code
        self.status_code = status_code

    def __str__(self) -> str:
        return f"[{self.code}] {super().__str__()} (HTTP {self.status_code})"

    def __repr__(self) -> str:
        return (
            f"APIError(status_code={self.status_code!r}, "
            f"code={self.code!r}, message={super().__str__()!r})"
        )


class ValidationError(APIError):
    """Raised when request validation fails (400)."""


class UnauthorizedError(APIError):
    """Raised when authentication fails (401)."""


class NotFoundError(APIError):
    """Raised when a resource is not found (404)."""


class ConflictError(APIError):
    """Raised when there is a resource conflict (409)."""


class PolicyError(APIError):
    """Raised when an action is denied by policy."""

    def __init__(self, message: str, rule_id: str = ""):
        super().__init__(message, code="policy_denied", status_code=403)
        self.rule_id = rule_id

    def __repr__(self) -> str:
        return f"PolicyError(reason={Exception.__str__(self)!r})"


class ToolError(RebunoError):
    def __init__(
        self,
        message: str,
        tool_id: str = "",
        step_id: str = "",
        retryable: bool = False,
    ):
        super().__init__(message)
        self.tool_id = tool_id
        self.step_id = step_id
        self.retryable = retryable

    def __repr__(self) -> str:
        return (
            f"ToolError(tool_id={self.tool_id!r}, step_id={self.step_id!r})"
        )
