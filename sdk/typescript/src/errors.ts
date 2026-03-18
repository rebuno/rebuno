export class RebunoError extends Error {
  details: Record<string, unknown>;

  constructor(message: string, details?: Record<string, unknown>) {
    super(message);
    this.name = "RebunoError";
    this.details = details ?? {};
  }
}

export class NetworkError extends RebunoError {
  constructor(message: string, details?: Record<string, unknown>) {
    super(message, details);
    this.name = "NetworkError";
  }
}

export class APIError extends RebunoError {
  code: string;
  statusCode: number;

  constructor(
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) {
    super(message, details);
    this.name = "APIError";
    this.code = code;
    this.statusCode = statusCode;
  }

  override toString(): string {
    return `[${this.code}] ${this.message} (HTTP ${this.statusCode})`;
  }
}

export class ValidationError extends APIError {
  constructor(
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) {
    super(message, code, statusCode, details);
    this.name = "ValidationError";
  }
}

export class UnauthorizedError extends APIError {
  constructor(
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) {
    super(message, code, statusCode, details);
    this.name = "UnauthorizedError";
  }
}

export class NotFoundError extends APIError {
  constructor(
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) {
    super(message, code, statusCode, details);
    this.name = "NotFoundError";
  }
}

export class ConflictError extends APIError {
  constructor(
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) {
    super(message, code, statusCode, details);
    this.name = "ConflictError";
  }
}

export class PolicyError extends APIError {
  ruleId: string;

  constructor(message: string, ruleId = "") {
    super(message, "policy_denied", 403);
    this.name = "PolicyError";
    this.ruleId = ruleId;
  }
}

export class ToolError extends RebunoError {
  toolId: string;
  stepId: string;
  retryable: boolean;

  constructor(
    message: string,
    toolId = "",
    stepId = "",
    retryable = false,
  ) {
    super(message);
    this.name = "ToolError";
    this.toolId = toolId;
    this.stepId = stepId;
    this.retryable = retryable;
  }
}
