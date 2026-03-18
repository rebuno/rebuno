import { describe, expect, it, vi } from "vitest";
import { RebunoClient } from "../src/client.js";
import {
  APIError,
  NotFoundError,
  PolicyError,
  ValidationError,
} from "../src/errors.js";
import { ExecutionStatus } from "../src/models.js";

function mockFetch(
  responses: Array<{
    status: number;
    body?: Record<string, unknown>;
    headers?: Record<string, string>;
  }>,
): typeof fetch {
  let callIndex = 0;
  return vi.fn(async () => {
    const resp = responses[callIndex++] ?? responses[responses.length - 1];
    return new Response(JSON.stringify(resp.body ?? {}), {
      status: resp.status,
      headers: {
        "Content-Type": "application/json",
        ...(resp.headers ?? {}),
      },
    });
  }) as unknown as typeof fetch;
}

describe("RebunoClient", () => {
  it("creates executions", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 200,
          body: {
            id: "exec-1",
            status: "pending",
            agent_id: "agent-1",
            labels: {},
          },
        },
      ]),
    });

    const exec = await client.createExecution("agent-1", { task: "test" });
    expect(exec.id).toBe("exec-1");
    expect(exec.status).toBe(ExecutionStatus.Pending);
    expect(exec.agentId).toBe("agent-1");
  });

  it("gets execution by id", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 200,
          body: {
            id: "exec-1",
            status: "running",
            agent_id: "agent-1",
            labels: { env: "test" },
          },
        },
      ]),
    });

    const exec = await client.getExecution("exec-1");
    expect(exec.id).toBe("exec-1");
    expect(exec.labels).toEqual({ env: "test" });
  });

  it("lists executions", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 200,
          body: {
            executions: [
              {
                id: "exec-1",
                status: "completed",
                agent_id: "agent-1",
                created_at: "2024-01-01T00:00:00Z",
                updated_at: "2024-01-01T00:01:00Z",
              },
            ],
            next_cursor: "",
          },
        },
      ]),
    });

    const result = await client.listExecutions({
      status: ExecutionStatus.Completed,
    });
    expect(result.executions).toHaveLength(1);
    expect(result.executions[0].id).toBe("exec-1");
  });

  it("throws ValidationError on 400", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 400,
          body: { error: "invalid input", code: "VALIDATION_ERROR" },
        },
      ]),
    });

    await expect(client.createExecution("")).rejects.toThrow(ValidationError);
  });

  it("throws NotFoundError on 404", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 404,
          body: { error: "not found", code: "NOT_FOUND" },
        },
      ]),
    });

    await expect(client.getExecution("missing")).rejects.toThrow(NotFoundError);
  });

  it("throws PolicyError on 403", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 403,
          body: { error: "denied", rule_id: "r1" },
        },
      ]),
    });

    await expect(client.getExecution("exec-1")).rejects.toThrow(PolicyError);
  });

  it("retries on 429", async () => {
    const fetchFn = mockFetch([
      { status: 429, body: {}, headers: { "Retry-After": "0.01" } },
      {
        status: 200,
        body: {
          id: "exec-1",
          status: "pending",
          agent_id: "agent-1",
          labels: {},
        },
      },
    ]);

    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: fetchFn,
    });

    const exec = await client.createExecution("agent-1");
    expect(exec.id).toBe("exec-1");
    expect(fetchFn).toHaveBeenCalledTimes(2);
  });

  it("retries GET on 5xx", async () => {
    const fetchFn = mockFetch([
      { status: 500, body: { error: "internal", code: "INTERNAL" } },
      {
        status: 200,
        body: {
          id: "exec-1",
          status: "running",
          agent_id: "agent-1",
          labels: {},
        },
      },
    ]);

    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: fetchFn,
      retryBaseDelay: 0.001,
    });

    const exec = await client.getExecution("exec-1");
    expect(exec.id).toBe("exec-1");
    expect(fetchFn).toHaveBeenCalledTimes(2);
  });

  it("does not retry POST on 5xx (non-idempotent)", async () => {
    const fetchFn = mockFetch([
      { status: 500, body: { error: "internal", code: "INTERNAL" } },
    ]);

    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: fetchFn,
    });

    await expect(client.createExecution("agent-1")).rejects.toThrow(APIError);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  it("sends signal", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([{ status: 200, body: { status: "ok" } }]),
    });

    const result = await client.sendSignal("exec-1", "human_input", {
      text: "hi",
    });
    expect(result.status).toBe("ok");
  });

  it("submits intent", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 200,
          body: {
            accepted: true,
            step_id: "step-1",
            error: "",
            pending_approval: false,
          },
        },
      ]),
    });

    const result = await client.submitIntent({
      executionId: "exec-1",
      sessionId: "session-1",
      intentType: "invoke_tool",
      toolId: "web.search",
      arguments: { query: "test" },
    });

    expect(result.accepted).toBe(true);
    expect(result.stepId).toBe("step-1");
  });

  it("converts snake_case response to camelCase", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([
        {
          status: 200,
          body: {
            id: "exec-1",
            status: "pending",
            agent_id: "agent-1",
            labels: {},
            created_at: "2024-01-01T00:00:00Z",
            updated_at: "2024-01-01T00:00:00Z",
          },
        },
      ]),
    });

    const exec = await client.getExecution("exec-1");
    expect(exec.agentId).toBe("agent-1");
    expect(exec.createdAt).toBe("2024-01-01T00:00:00Z");
  });

  it("converts camelCase request to snake_case", async () => {
    let capturedBody = "";
    const fetchFn: typeof fetch = vi.fn(
      async (_url: string | URL | Request, init?: RequestInit) => {
        capturedBody = init?.body as string;
        return new Response(
          JSON.stringify({
            accepted: true,
            step_id: "s1",
            error: "",
            pending_approval: false,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      },
    ) as unknown as typeof fetch;

    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: fetchFn,
    });

    await client.submitIntent({
      executionId: "exec-1",
      sessionId: "sess-1",
      intentType: "invoke_tool",
      toolId: "test",
    });

    const parsed = JSON.parse(capturedBody);
    expect(parsed.execution_id).toBe("exec-1");
    expect(parsed.session_id).toBe("sess-1");
    expect(parsed.intent.tool_id).toBe("test");
  });

  it("health check works", async () => {
    const client = new RebunoClient({
      baseUrl: "http://localhost:8080",
      fetch: mockFetch([{ status: 200, body: { status: "ok" } }]),
    });

    const result = await client.health();
    expect(result.status).toBe("ok");
  });
});
