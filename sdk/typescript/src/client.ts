import { USER_AGENT } from "./version.js";
import { camelKeys, snakeKeys } from "./internal/fetch.js";
import { jitteredBackoff } from "./internal/backoff.js";
import { parseSSEStream, type SSEEvent } from "./sse.js";
import {
  APIError,
  ConflictError,
  NetworkError,
  NotFoundError,
  PolicyError,
  RebunoError,
  UnauthorizedError,
  ValidationError,
} from "./errors.js";
import type {
  Event,
  EventList,
  Execution,
  ExecutionStatus,
  IntentResult,
  ListExecutionsResult,
  SignalResult,
} from "./models.js";

const STATUS_TO_ERROR: Record<
  number,
  new (
    message: string,
    code: string,
    statusCode: number,
    details?: Record<string, unknown>,
  ) => APIError
> = {
  400: ValidationError,
  401: UnauthorizedError,
  404: NotFoundError,
  409: ConflictError,
};

function apiError(status: number, body: Record<string, unknown>): APIError {
  const message =
    (body.error as string) || (body.message as string) || "Unknown error";
  const code = (body.code as string) || "UNKNOWN";
  const details = body.details as Record<string, unknown> | undefined;

  const ErrorClass = STATUS_TO_ERROR[status] ?? APIError;
  return new ErrorClass(message, code, status, details);
}

export type FetchFn = typeof globalThis.fetch;

export interface RebunoClientOptions {
  baseUrl: string;
  apiKey?: string;
  timeout?: number;
  maxRetries?: number;
  retryBaseDelay?: number;
  retryMaxDelay?: number;
  fetch?: FetchFn;
}

export class RebunoClient {
  readonly baseUrl: string;
  readonly apiKey: string;
  readonly maxRetries: number;
  readonly retryBaseDelay: number;
  readonly retryMaxDelay: number;
  private readonly timeout: number;
  private readonly headers: Record<string, string>;
  private readonly fetchFn: FetchFn;

  constructor(options: RebunoClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.apiKey = options.apiKey ?? "";
    this.timeout = options.timeout ?? 35000;
    this.maxRetries = options.maxRetries ?? 3;
    this.retryBaseDelay = options.retryBaseDelay ?? 1.0;
    this.retryMaxDelay = options.retryMaxDelay ?? 10.0;
    this.fetchFn = options.fetch ?? globalThis.fetch;

    this.headers = {
      "Content-Type": "application/json",
      "User-Agent": USER_AGENT,
    };
    if (this.apiKey) {
      this.headers["Authorization"] = `Bearer ${this.apiKey}`;
    }
  }

  private retryDelay(attempt: number): number {
    return jitteredBackoff(this.retryBaseDelay, attempt + 1, this.retryMaxDelay);
  }

  private async request(
    method: string,
    path: string,
    options?: {
      json?: Record<string, unknown>;
      params?: Record<string, unknown>;
      idempotent?: boolean;
      skipSnakeKeys?: boolean;
    },
  ): Promise<Record<string, unknown>> {
    const retryable = method === "GET" || options?.idempotent === true;
    let lastError: Error | null = null;

    for (let attempt = 0; attempt <= this.maxRetries; attempt++) {
      let url = `${this.baseUrl}${path}`;
      if (options?.params) {
        const searchParams = new URLSearchParams();
        for (const [key, value] of Object.entries(options.params)) {
          if (value !== undefined && value !== null && value !== "") {
            searchParams.set(key, String(value));
          }
        }
        const qs = searchParams.toString();
        if (qs) url += `?${qs}`;
      }

      const init: RequestInit = {
        method,
        headers: { ...this.headers },
        signal: AbortSignal.timeout(this.timeout),
      };

      if (options?.json) {
        init.body = JSON.stringify(
          options.skipSnakeKeys ? options.json : snakeKeys(options.json),
        );
      }

      let resp: Response;
      try {
        resp = await this.fetchFn(url, init);
      } catch (err) {
        lastError = new NetworkError(
          err instanceof Error ? err.message : String(err),
        );
        if (!retryable || attempt >= this.maxRetries) break;
        const delay = this.retryDelay(attempt);
        await sleep(delay * 1000);
        continue;
      }

      if (resp.status === 429) {
        if (attempt >= this.maxRetries) {
          const body = await safeJson(resp);
          throw apiError(resp.status, body);
        }
        const retryAfter = parseFloat(
          resp.headers.get("Retry-After") ?? "1",
        );
        await sleep((isNaN(retryAfter) ? 1 : retryAfter) * 1000);
        continue;
      }

      if (resp.status === 403) {
        const body = await safeJson(resp);
        throw new PolicyError(
          (body.error as string) || "Forbidden",
          (body.rule_id as string) || "",
        );
      }

      if (resp.status >= 500 && retryable) {
        const body = await safeJson(resp);
        lastError = apiError(resp.status, body);
        if (attempt >= this.maxRetries) break;
        const delay = this.retryDelay(attempt);
        await sleep(delay * 1000);
        continue;
      }

      if (resp.status >= 400) {
        const body = await safeJson(resp);
        throw apiError(resp.status, body);
      }

      const body = await resp.json();
      return camelKeys(body) as Record<string, unknown>;
    }

    throw lastError ?? new RebunoError("max retries exceeded");
  }

  async createExecution(
    agentId: string,
    input?: unknown,
    labels?: Record<string, string>,
  ): Promise<Execution> {
    const body: Record<string, unknown> = { agentId };
    if (input !== undefined) body.input = input;
    if (labels) body.labels = labels;
    return (await this.request("POST", "/v0/executions", {
      json: body,
    })) as unknown as Execution;
  }

  async getExecution(executionId: string): Promise<Execution> {
    return (await this.request(
      "GET",
      `/v0/executions/${executionId}`,
    )) as unknown as Execution;
  }

  async listExecutions(options?: {
    status?: ExecutionStatus;
    agentId?: string;
    limit?: number;
    cursor?: string;
  }): Promise<ListExecutionsResult> {
    const params: Record<string, unknown> = {
      limit: options?.limit ?? 50,
    };
    if (options?.status) params.status = options.status;
    if (options?.agentId) params.agent_id = options.agentId;
    if (options?.cursor) params.cursor = options.cursor;
    return (await this.request("GET", "/v0/executions", {
      params,
    })) as unknown as ListExecutionsResult;
  }

  async cancelExecution(executionId: string): Promise<Execution> {
    return (await this.request(
      "POST",
      `/v0/executions/${executionId}/cancel`,
      { idempotent: true },
    )) as unknown as Execution;
  }

  async sendSignal(
    executionId: string,
    signalType: string,
    payload?: unknown,
  ): Promise<SignalResult> {
    const body: Record<string, unknown> = { signalType };
    if (payload !== undefined) body.payload = payload;
    return (await this.request(
      "POST",
      `/v0/executions/${executionId}/signal`,
      { json: body },
    )) as unknown as SignalResult;
  }

  async getEvents(
    executionId: string,
    afterSequence = 0,
    limit = 100,
  ): Promise<EventList> {
    return (await this.request(
      "GET",
      `/v0/executions/${executionId}/events`,
      { params: { after_sequence: afterSequence, limit } },
    )) as unknown as EventList;
  }

  async *streamEvents(
    executionId: string,
    afterSequence = 0,
  ): AsyncGenerator<Event> {
    const params: Record<string, string> = {};
    if (afterSequence) params.after_sequence = String(afterSequence);

    const qs = new URLSearchParams(params).toString();
    const url = `${this.baseUrl}/v0/executions/${executionId}/stream${qs ? `?${qs}` : ""}`;

    const resp = await this.fetchFn(url, {
      headers: {
        ...this.headers,
        Accept: "text/event-stream",
      },
    });

    if (!resp.ok) {
      const body = await safeJson(resp);
      throw apiError(resp.status, body);
    }

    if (!resp.body) throw new RebunoError("No response body for SSE stream");

    for await (const sse of parseSSEStream(resp.body)) {
      yield camelKeys(JSON.parse(sse.data)) as unknown as Event;
    }
  }

  async submitIntent(options: {
    executionId: string;
    sessionId: string;
    intentType: string;
    toolId?: string;
    arguments?: unknown;
    idempotencyKey?: string;
    signalType?: string;
    output?: unknown;
    error?: string;
    remote?: boolean;
  }): Promise<IntentResult> {
    const intent: Record<string, unknown> = { type: options.intentType };
    if (options.toolId) intent.toolId = options.toolId;
    if (options.arguments !== undefined) intent.arguments = options.arguments;
    if (options.idempotencyKey) intent.idempotencyKey = options.idempotencyKey;
    if (options.signalType) intent.signalType = options.signalType;
    if (options.output !== undefined) intent.output = options.output;
    if (options.error) intent.error = options.error;
    if (options.remote) intent.remote = true;

    // Preserve arguments/output as-is since those are user-defined tool data
    const userDataKeys = ["arguments", "output"];
    const snaked = snakeKeys({
      executionId: options.executionId,
      sessionId: options.sessionId,
      intent,
    }) as Record<string, unknown>;
    const snakedIntent = snaked.intent as Record<string, unknown>;
    for (const key of userDataKeys) {
      if (key in intent) {
        snakedIntent[key] = intent[key];
      }
    }

    return (await this.request("POST", "/v0/agents/intent", {
      json: snaked,
      skipSnakeKeys: true,
    })) as unknown as IntentResult;
  }

  async reportStepResult(options: {
    executionId: string;
    sessionId: string;
    stepId: string;
    success: boolean;
    data?: unknown;
    error?: string;
  }): Promise<void> {
    const snaked = snakeKeys({
      executionId: options.executionId,
      sessionId: options.sessionId,
      stepId: options.stepId,
      success: options.success,
    }) as Record<string, unknown>;
    if (options.data !== undefined) snaked.data = options.data;
    if (options.error) snaked.error = options.error;
    await this.request("POST", "/v0/agents/step-result", {
      json: snaked,
      skipSnakeKeys: true,
      idempotent: true,
    });
  }

  async submitResult(options: {
    runnerId: string;
    jobId: string;
    executionId: string;
    stepId: string;
    success: boolean;
    data?: unknown;
    error?: string;
    retryable?: boolean;
    startedAt?: string;
    completedAt?: string;
  }): Promise<Record<string, unknown>> {
    const snaked = snakeKeys({
      jobId: options.jobId,
      executionId: options.executionId,
      stepId: options.stepId,
      success: options.success,
      retryable: options.retryable,
      startedAt: options.startedAt,
      completedAt: options.completedAt,
    }) as Record<string, unknown>;
    if (options.data !== undefined) snaked.data = options.data;
    if (options.error) snaked.error = options.error;

    return await this.request(
      "POST",
      `/v0/runners/${options.runnerId}/results`,
      { json: snaked, skipSnakeKeys: true, idempotent: true },
    );
  }

  async stepStarted(
    stepId: string,
    executionId: string,
    runnerId: string,
  ): Promise<void> {
    await this.request("POST", `/v0/runners/steps/${stepId}/started`, {
      json: { executionId, runnerId },
    });
  }

  async updateCapabilities(
    runnerId: string,
    tools: string[],
  ): Promise<void> {
    await this.request("POST", `/v0/runners/${runnerId}/capabilities`, {
      json: { tools },
    });
  }

  async unregisterRunner(runnerId: string): Promise<void> {
    await this.request("DELETE", `/v0/runners/${runnerId}`);
  }

  async *agentStream(
    agentId: string,
    consumerId: string,
  ): AsyncGenerator<SSEEvent> {
    const params = new URLSearchParams({
      agent_id: agentId,
      consumer_id: consumerId,
    });

    const resp = await this.fetchFn(
      `${this.baseUrl}/v0/agents/stream?${params}`,
      {
        headers: {
          ...this.headers,
          Accept: "text/event-stream",
        },
      },
    );

    if (!resp.ok) {
      const body = await safeJson(resp);
      throw apiError(resp.status, body);
    }

    if (!resp.body) throw new RebunoError("No response body for SSE stream");

    yield* parseSSEStream(resp.body);
  }

  async *runnerStream(
    runnerId: string,
    consumerId: string,
    capabilities?: string[],
  ): AsyncGenerator<SSEEvent> {
    const params = new URLSearchParams({
      runner_id: runnerId,
      consumer_id: consumerId,
    });
    if (capabilities?.length) {
      params.set("capabilities", capabilities.join(","));
    }

    const resp = await this.fetchFn(
      `${this.baseUrl}/v0/runners/stream?${params}`,
      {
        headers: {
          ...this.headers,
          Accept: "text/event-stream",
        },
      },
    );

    if (!resp.ok) {
      const body = await safeJson(resp);
      throw apiError(resp.status, body);
    }

    if (!resp.body) throw new RebunoError("No response body for SSE stream");

    yield* parseSSEStream(resp.body);
  }

  async health(): Promise<Record<string, string>> {
    return (await this.request(
      "GET",
      "/v0/health",
    )) as unknown as Record<string, string>;
  }

  async ready(): Promise<Record<string, string>> {
    return (await this.request(
      "GET",
      "/v0/ready",
    )) as unknown as Record<string, string>;
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function safeJson(resp: Response): Promise<Record<string, unknown>> {
  try {
    return await resp.json();
  } catch {
    return {};
  }
}
