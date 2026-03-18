export enum ExecutionStatus {
  Pending = "pending",
  Running = "running",
  Blocked = "blocked",
  Completed = "completed",
  Failed = "failed",
  Cancelled = "cancelled",
}

export enum StepStatus {
  Pending = "pending",
  Dispatched = "dispatched",
  Running = "running",
  Succeeded = "succeeded",
  Failed = "failed",
  TimedOut = "timed_out",
  Cancelled = "cancelled",
}

export interface Execution {
  id: string;
  status: ExecutionStatus;
  agentId: string;
  labels: Record<string, string>;
  input?: unknown;
  output?: unknown;
  createdAt?: string;
  updatedAt?: string;
}

export interface Event {
  id: string;
  executionId: string;
  stepId: string;
  type: string;
  schemaVersion: number;
  timestamp?: string;
  payload?: unknown;
  sequence: number;
  idempotencyKey: string;
  causationId: string;
  correlationId: string;
}

export interface Step {
  id: string;
  executionId: string;
  toolId: string;
  toolVersion: number;
  status: StepStatus;
  attempt: number;
  maxAttempts: number;
  arguments?: unknown;
  result?: unknown;
  error: string;
  retryable: boolean;
  idempotencyKey: string;
  deadline?: string;
  runnerId: string;
  createdAt?: string;
  dispatchedAt?: string;
  startedAt?: string;
  completedAt?: string;
}

export interface Intent {
  type: string;
  toolId: string;
  arguments?: unknown;
  idempotencyKey: string;
  signalType: string;
  output?: unknown;
  error: string;
  remote: boolean;
}

export interface IntentResult {
  accepted: boolean;
  stepId: string;
  error: string;
  pendingApproval: boolean;
}

export interface Job {
  id: string;
  executionId: string;
  stepId: string;
  attempt: number;
  toolId: string;
  toolVersion: number;
  arguments?: unknown;
  deadline?: string;
}

export interface JobResult {
  jobId: string;
  executionId: string;
  stepId: string;
  success: boolean;
  data?: unknown;
  error: string;
  retryable: boolean;
  startedAt?: string;
  completedAt?: string;
  runnerId: string;
}

export interface Signal {
  id: string;
  executionId: string;
  signalType: string;
  payload?: unknown;
  createdAt: string;
}

export interface ToolSummary {
  id: string;
  version: number;
  name: string;
  description: string;
}

export interface HistoryEntry {
  stepId: string;
  toolId: string;
  status: StepStatus;
  arguments?: unknown;
  result?: unknown;
  error: string;
  completedAt?: string;
}

export interface ClaimResult {
  executionId: string;
  sessionId: string;
  agentId: string;
  input?: unknown;
  labels: Record<string, string>;
  history: HistoryEntry[];
}

export interface ExecutionSummary {
  id: string;
  status: ExecutionStatus;
  agentId: string;
  createdAt: string;
  updatedAt: string;
}

export interface ListExecutionsResult {
  executions: ExecutionSummary[];
  nextCursor: string;
}

export interface EventList {
  events: Event[];
  latestSequence: number;
}

export interface SignalResult {
  status: string;
}
