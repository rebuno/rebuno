export interface Execution {
  id: string;
  status: string;
  agent_id: string;
  created_at: string;
  updated_at: string;
}

export interface Event {
  id: string;
  execution_id: string;
  sequence: number;
  type: string;
  step_id?: string;
  schema_version: number;
  timestamp: string;
  payload?: Record<string, unknown>;
  causation_id?: string;
  correlation_id?: string;
  idempotency_key?: string;
}

export interface ExecutionDetail {
  id: string;
  agent_id: string;
  status: string;
  input?: unknown;
  output?: unknown;
  labels?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

async function request(method: string, path: string, body?: unknown) {
  const opts: RequestInit = {
    method,
    headers: { "Content-Type": "application/json" },
  };
  if (body) opts.body = JSON.stringify(body);
  const resp = await fetch(path, opts);
  if (resp.status === 204) return null;
  if (!resp.ok) {
    const err = await resp.json().catch(() => ({ error: resp.statusText }));
    throw new Error(err.error || resp.statusText);
  }
  return resp.json();
}

export async function checkHealth(): Promise<boolean> {
  try {
    await request("GET", "/v0/health");
    return true;
  } catch {
    return false;
  }
}

export async function listExecutions(params?: {
  status?: string;
  agent_id?: string;
  cursor?: string;
  limit?: number;
}): Promise<{ executions: Execution[]; next_cursor: string }> {
  const qs = new URLSearchParams();
  if (params?.status) qs.set("status", params.status);
  if (params?.agent_id) qs.set("agent_id", params.agent_id);
  if (params?.cursor) qs.set("cursor", params.cursor);
  if (params?.limit != null) qs.set("limit", String(params.limit));

  const query = qs.toString();
  const data = await request("GET", `/v0/executions${query ? `?${query}` : ""}`);
  return { executions: data?.executions ?? [], next_cursor: data?.next_cursor ?? "" };
}

export async function createExecution(
  agentId: string,
  input: unknown,
  labels?: Record<string, string>
): Promise<ExecutionDetail> {
  return request("POST", "/v0/executions", {
    agent_id: agentId,
    input,
    ...(labels && Object.keys(labels).length > 0 && { labels }),
  });
}

export async function getExecution(id: string): Promise<ExecutionDetail> {
  return request("GET", `/v0/executions/${id}`);
}

export async function cancelExecution(id: string): Promise<void> {
  await request("POST", `/v0/executions/${id}/cancel`);
}

export async function getEvents(
  executionId: string,
  afterSequence = 0,
  limit = 500
): Promise<{ events: Event[]; latest_sequence: number }> {
  const data = await request(
    "GET",
    `/v0/executions/${executionId}/events?after_sequence=${afterSequence}&limit=${limit}`
  );
  return { events: data?.events ?? [], latest_sequence: data?.latest_sequence ?? 0 };
}

export async function sendSignal(
  executionId: string,
  signalType: string,
  payload: unknown
): Promise<void> {
  await request("POST", `/v0/executions/${executionId}/signal`, {
    signal_type: signalType,
    payload,
  });
}
