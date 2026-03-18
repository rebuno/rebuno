export { VERSION, USER_AGENT } from "./version.js";

export {
  RebunoError,
  NetworkError,
  APIError,
  ValidationError,
  UnauthorizedError,
  NotFoundError,
  ConflictError,
  PolicyError,
  ToolError,
} from "./errors.js";

export {
  ExecutionStatus,
  StepStatus,
} from "./models.js";
export type {
  Execution,
  Event,
  Step,
  Intent,
  IntentResult,
  Job,
  JobResult,
  Signal,
  ToolSummary,
  HistoryEntry,
  ClaimResult,
  ExecutionSummary,
  ListExecutionsResult,
  EventList,
  SignalResult,
} from "./models.js";

export { RebunoClient } from "./client.js";
export type { RebunoClientOptions, FetchFn } from "./client.js";

export { AgentContext, BaseAgent } from "./agent.js";
export type { BaseAgentOptions } from "./agent.js";

export { BaseRunner } from "./runner.js";
export type { BaseRunnerOptions } from "./runner.js";

export { defineTool } from "./tools/index.js";
export type { RebunoTool, WrappedTool } from "./tools/index.js";
export { ToolRegistry } from "./tools/registry.js";
export type { ToolFormat } from "./tools/registry.js";

export { parseSSEStream } from "./sse.js";
export type { SSEEvent } from "./sse.js";

export { McpConnection, McpManager } from "./mcp.js";
export type { McpServerConfig, McpToolInfo } from "./mcp.js";
