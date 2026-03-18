import { randomUUID } from "node:crypto";
import { z } from "zod";
import { RebunoClient } from "./client.js";
import { camelKeys } from "./internal/fetch.js";
import { jitteredBackoff } from "./internal/backoff.js";
import { parseSSEStream, type SSEEvent } from "./sse.js";
import { PolicyError, RebunoError, ToolError } from "./errors.js";
import type { ClaimResult, HistoryEntry } from "./models.js";
import type { RebunoTool, WrappedTool } from "./tools/index.js";
import { ToolRegistry } from "./tools/registry.js";
import type { McpServerConfig, McpToolInfo, McpManager } from "./mcp.js";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function jsonSchemaToZod(schema: Record<string, unknown>): z.ZodType {
  const properties = schema.properties as Record<string, Record<string, unknown>> | undefined;
  if (!properties) return z.record(z.unknown());

  const required = new Set((schema.required as string[]) ?? []);
  const shape: Record<string, z.ZodType> = {};

  for (const [key, prop] of Object.entries(properties)) {
    let field: z.ZodType;
    switch (prop.type) {
      case "string":
        field = z.string();
        break;
      case "number":
      case "integer":
        field = z.number();
        break;
      case "boolean":
        field = z.boolean();
        break;
      case "array":
        field = z.array(z.unknown());
        break;
      case "object":
        field = z.record(z.unknown());
        break;
      default:
        field = z.unknown();
    }
    if (prop.description) {
      field = (field as z.ZodString).describe(prop.description as string);
    }
    if (!required.has(key)) {
      field = field.nullable().optional();
    }
    shape[key] = field;
  }

  return z.object(shape);
}

export class AgentContext {
  readonly executionId: string;
  readonly sessionId: string;
  readonly agentId: string;
  readonly input: unknown;
  readonly labels: Record<string, string>;
  readonly history: HistoryEntry[];

  private client: RebunoClient;
  private localTools: Map<string, (args: unknown) => Promise<unknown>>;
  private remoteToolFns: Map<string, (args: unknown) => Promise<unknown>>;
  private registry: ToolRegistry;
  private mcpToolInfos: McpToolInfo[];
  private localResults = new Map<string, unknown>();
  private resultDeferreds = new Map<string, Deferred<Record<string, unknown>>>();
  private signalDeferreds = new Map<string, Deferred<unknown>>();
  private approvalDeferreds = new Map<string, Deferred<Record<string, unknown>>>();
  private waitTimeout: number;

  constructor(
    client: RebunoClient,
    claim: ClaimResult,
    registry: ToolRegistry,
    localTools: Map<string, (args: unknown) => Promise<unknown>>,
    remoteTools: Map<string, (args: unknown) => Promise<unknown>>,
    mcpToolInfos: McpToolInfo[] = [],
    waitTimeout = 3600000,
  ) {
    this.client = client;
    this.executionId = claim.executionId;
    this.sessionId = claim.sessionId;
    this.agentId = claim.agentId;
    this.input = claim.input;
    this.labels = claim.labels;
    this.history = claim.history;
    this.registry = registry;
    this.localTools = localTools;
    this.remoteToolFns = remoteTools;
    this.mcpToolInfos = mcpToolInfos;
    this.waitTimeout = waitTimeout;
  }

  _dispatchResult(stepId: string, data: Record<string, unknown>): void {
    this.resultDeferreds.get(stepId)?.resolve(data);
  }

  _dispatchSignal(signalType: string, payload: unknown): void {
    this.signalDeferreds.get(signalType)?.resolve(payload);
  }

  _dispatchApproval(stepId: string, data: Record<string, unknown>): void {
    this.approvalDeferreds.get(stepId)?.resolve(data);
  }

  private isLocal(toolId: string): boolean {
    return this.localTools.has(toolId);
  }

  async invokeTool(
    toolId: string,
    args?: unknown,
    idempotencyKey?: string,
  ): Promise<unknown> {
    if (!idempotencyKey) {
      idempotencyKey = `${this.executionId}:${toolId}:${randomUUID().slice(0, 8)}`;
    }

    const local = this.isLocal(toolId);

    const result = await this.client.submitIntent({
      executionId: this.executionId,
      sessionId: this.sessionId,
      intentType: "invoke_tool",
      toolId,
      arguments: args,
      idempotencyKey,
      remote: !local,
    });

    if (!result.accepted) {
      throw new PolicyError(result.error || "Intent denied by policy");
    }

    const stepId = result.stepId;
    if (!stepId) {
      throw new RebunoError("No step_id returned for invoke_tool intent");
    }

    if (result.pendingApproval) {
      const approval = await this.waitForApproval(stepId);
      if (!approval.approved) {
        throw new PolicyError("Tool invocation denied by human approval");
      }
    }

    if (local) {
      return this.executeLocal(stepId, toolId, args);
    }
    return this.waitForResult(stepId, toolId);
  }

  getTools(): WrappedTool[] {
    const registryTools = this.registry.getWrappedTools().map((tool) => ({
      ...tool,
      execute: (input: unknown) => this.invokeTool(tool.id, input),
    }));

    const seenIds = new Set(registryTools.map((t) => t.id));

    const mcpWrapped: WrappedTool[] = this.mcpToolInfos
      .filter((t) => !seenIds.has(t.id))
      .map((t) => {
        seenIds.add(t.id);
        const schema = t.inputSchema?.properties
          ? jsonSchemaToZod(t.inputSchema)
          : z.record(z.unknown());
        return {
          id: t.id,
          description: t.description,
          inputSchema: schema,
          execute: (input: unknown) => this.invokeTool(t.id, input),
        };
      });

    const remoteWrapped: WrappedTool[] = [];
    for (const [id] of this.remoteToolFns) {
      if (!seenIds.has(id)) {
        remoteWrapped.push({
          id,
          description: "",
          inputSchema: z.record(z.unknown()),
          execute: (input: unknown) => this.invokeTool(id, input),
        });
      }
    }

    return [...registryTools, ...mcpWrapped, ...remoteWrapped];
  }

  async submitTool(
    toolId: string,
    args?: unknown,
    idempotencyKey?: string,
  ): Promise<string> {
    if (!idempotencyKey) {
      idempotencyKey = `${this.executionId}:${toolId}:${randomUUID().slice(0, 8)}`;
    }

    const local = this.isLocal(toolId);

    const result = await this.client.submitIntent({
      executionId: this.executionId,
      sessionId: this.sessionId,
      intentType: "invoke_tool",
      toolId,
      arguments: args,
      idempotencyKey,
      remote: !local,
    });

    if (!result.accepted) {
      throw new PolicyError(result.error || "Intent denied by policy");
    }

    if (!result.stepId) {
      throw new RebunoError("No step_id returned for invoke_tool intent");
    }

    if (local) {
      const output = await this.executeLocal(result.stepId, toolId, args);
      this.localResults.set(result.stepId, output);
    }

    return result.stepId;
  }

  async awaitSteps(stepIds: string[]): Promise<unknown[]> {
    const results = new Map<string, unknown>();
    const remoteIds: string[] = [];

    for (const stepId of stepIds) {
      if (this.localResults.has(stepId)) {
        results.set(stepId, this.localResults.get(stepId));
        this.localResults.delete(stepId);
      } else {
        remoteIds.push(stepId);
      }
    }

    if (remoteIds.length > 0) {
      const waitPromises = remoteIds.map(async (sid) => {
        const data = await this.waitForEvent<Record<string, unknown>>(
          sid,
          this.resultDeferreds,
        );
        if (data.status === "failed") {
          throw new ToolError(
            (data.error as string) || "Tool execution failed",
            "",
            sid,
          );
        }
        return [sid, data.result] as const;
      });

      const remoteResults = await Promise.all(waitPromises);
      for (const [sid, val] of remoteResults) {
        results.set(sid, val);
      }
    }

    return stepIds.map((sid) => results.get(sid));
  }

  async waitSignal(signalType: string): Promise<unknown> {
    const result = await this.client.submitIntent({
      executionId: this.executionId,
      sessionId: this.sessionId,
      intentType: "wait",
      signalType,
    });

    if (!result.accepted) {
      throw new PolicyError(result.error || "Wait intent denied");
    }

    return this.waitForEvent(signalType, this.signalDeferreds);
  }

  async complete(output?: unknown): Promise<void> {
    await this.client.submitIntent({
      executionId: this.executionId,
      sessionId: this.sessionId,
      intentType: "complete",
      output,
    });
  }

  async fail(error: string): Promise<void> {
    await this.client.submitIntent({
      executionId: this.executionId,
      sessionId: this.sessionId,
      intentType: "fail",
      error,
    });
  }

  private async executeLocal(
    stepId: string,
    toolId: string,
    args: unknown,
  ): Promise<unknown> {
    const fn = this.localTools.get(toolId);
    if (!fn) throw new ToolError(`No local handler for tool '${toolId}'`, toolId, stepId);

    try {
      const output = await fn(args);
      await this.client.reportStepResult({
        executionId: this.executionId,
        sessionId: this.sessionId,
        stepId,
        success: true,
        data: output,
      });
      return output;
    } catch (err) {
      await this.client.reportStepResult({
        executionId: this.executionId,
        sessionId: this.sessionId,
        stepId,
        success: false,
        error: err instanceof Error ? err.message : String(err),
      });
      if (err instanceof ToolError) throw err;
      throw new ToolError(
        err instanceof Error ? err.message : String(err),
        toolId,
        stepId,
      );
    }
  }

  private async waitForEvent<T>(
    key: string,
    deferreds: Map<string, Deferred<T>>,
  ): Promise<T> {
    const d = deferred<T>();
    deferreds.set(key, d);

    const timeout = new Promise<never>((_, reject) =>
      setTimeout(
        () => reject(new RebunoError(`Timed out waiting for event (key=${key})`)),
        this.waitTimeout,
      ),
    );

    try {
      return await Promise.race([d.promise, timeout]);
    } finally {
      deferreds.delete(key);
    }
  }

  private async waitForResult(
    stepId: string,
    toolId: string,
  ): Promise<unknown> {
    const data = await this.waitForEvent<Record<string, unknown>>(
      stepId,
      this.resultDeferreds,
    );
    if (data.status === "failed") {
      throw new ToolError(
        (data.error as string) || "Tool execution failed",
        toolId,
        stepId,
      );
    }
    return data.result;
  }

  private async waitForApproval(stepId: string): Promise<Record<string, unknown>> {
    return this.waitForEvent(stepId, this.approvalDeferreds);
  }
}

export interface BaseAgentOptions {
  agentId: string;
  kernelUrl: string;
  apiKey?: string;
  consumerId?: string;
  reconnectDelay?: number;
  maxReconnectDelay?: number;
}

export abstract class BaseAgent {
  readonly agentId: string;
  readonly consumerId: string;
  readonly reconnectDelay: number;
  readonly maxReconnectDelay: number;

  protected client: RebunoClient;
  protected registry = new ToolRegistry();

  private tools = new Map<string, (args: unknown) => Promise<unknown>>();
  private remoteTools = new Map<string, (args: unknown) => Promise<unknown>>();
  private running = false;
  private abortController: AbortController | null = null;
  private contexts = new Map<string, AgentContext>();
  private mcpManager: McpManager | null = null;
  private mcpConfigs: McpServerConfig[] = [];

  constructor(options: BaseAgentOptions) {
    if (!options.agentId) throw new Error("agentId must not be empty");
    if (!options.kernelUrl) throw new Error("kernelUrl must not be empty");

    this.agentId = options.agentId;
    this.consumerId =
      options.consumerId ?? `${options.agentId}-${randomUUID().slice(0, 8)}`;
    this.reconnectDelay = options.reconnectDelay ?? 3.0;
    this.maxReconnectDelay = options.maxReconnectDelay ?? 60.0;

    this.client = new RebunoClient({
      baseUrl: options.kernelUrl,
      apiKey: options.apiKey,
    });
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  addTool(tool: RebunoTool<any, any>): void {
    this.registry.addTool(tool);
    this.tools.set(tool.id, tool.execute as (args: unknown) => Promise<unknown>);
  }

  addExternalTool(id: string, tool: unknown): void {
    this.registry.addExternalTool(id, tool);
    const execute = this.registry.getExecute(id);
    if (execute) this.tools.set(id, execute);
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  addRemoteTool(tool: RebunoTool<any, any>): void {
    this.registry.addTool(tool);
    this.remoteTools.set(tool.id, tool.execute as (args: unknown) => Promise<unknown>);
  }

  mcpServer(config: McpServerConfig): void {
    this.mcpConfigs.push(config);
  }

  mcpServersFromConfig(config: Record<string, unknown>): void {
    const servers = (config.mcpServers ?? config) as Record<
      string,
      Record<string, unknown>
    >;
    for (const [name, serverConfig] of Object.entries(servers)) {
      this.mcpConfigs.push({
        name,
        command: serverConfig.command as string | undefined,
        args: serverConfig.args as string[] | undefined,
        env: serverConfig.env as Record<string, string> | undefined,
        url: serverConfig.url as string | undefined,
        headers: serverConfig.headers as Record<string, string> | undefined,
      });
    }
  }

  abstract process(ctx: AgentContext): Promise<unknown>;

  async run(): Promise<void> {
    this.running = true;
    let consecutiveFailures = 0;

    try {
      while (this.running) {
        try {
          await this.connectAndProcess();
          consecutiveFailures = 0;
        } catch (err) {
          if (!this.running) break;
          consecutiveFailures++;
          const delay = jitteredBackoff(
            this.reconnectDelay,
            consecutiveFailures,
            this.maxReconnectDelay,
          );
          await sleep(delay * 1000);
        }
      }
    } finally {
      if (this.mcpManager) {
        await this.mcpManager.disconnectAll();
      }
    }
  }

  stop(): void {
    this.running = false;
    this.abortController?.abort();
  }

  private async connectAndProcess(): Promise<void> {
    await this.initMcp();

    this.abortController = new AbortController();

    const params = new URLSearchParams({
      agent_id: this.agentId,
      consumer_id: this.consumerId,
    });

    const resp = await this.client["fetchFn"](
      `${this.client.baseUrl}/v0/agents/stream?${params}`,
      {
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          ...(this.client.apiKey
            ? { Authorization: `Bearer ${this.client.apiKey}` }
            : {}),
        },
        signal: this.abortController.signal,
      },
    );

    if (!resp.ok) {
      throw new RebunoError(`SSE connection failed: ${resp.status}`);
    }

    if (!resp.body) throw new RebunoError("No response body");

    for await (const event of parseSSEStream(resp.body)) {
      if (!this.running) return;
      await this.handleEvent(event);
    }
  }

  private async handleEvent(event: SSEEvent): Promise<void> {
    const raw = JSON.parse(event.data);
    const data = camelKeys(raw) as Record<string, unknown>;

    switch (event.type) {
      case "execution.assigned": {
        const claim = data as unknown as ClaimResult;
        if (raw.input !== undefined) claim.input = raw.input;
        this.handleExecution(claim).catch(() => {});
        break;
      }
      case "tool.result": {
        if (raw.data !== undefined) data.data = raw.data;
        const ctx = this.contexts.get(data.executionId as string);
        ctx?._dispatchResult(data.stepId as string, data as Record<string, unknown>);
        break;
      }
      case "signal.received": {
        const ctx = this.contexts.get(data.executionId as string);
        ctx?._dispatchSignal(data.signalType as string, data.payload);
        break;
      }
      case "approval.resolved": {
        const ctx = this.contexts.get(data.executionId as string);
        ctx?._dispatchApproval(data.stepId as string, data as Record<string, unknown>);
        break;
      }
    }
  }

  private async handleExecution(claim: ClaimResult): Promise<void> {
    const mcpTools = new Map<string, (args: unknown) => Promise<unknown>>();
    let mcpToolInfos: McpToolInfo[] = [];
    if (this.mcpManager) {
      mcpToolInfos = await this.mcpManager.allTools();
      for (const t of mcpToolInfos) {
        mcpTools.set(t.id, (args: unknown) =>
          this.mcpManager!.callTool(t.id, args as Record<string, unknown>),
        );
      }
    }

    const allLocalTools = new Map([...this.tools, ...mcpTools]);

    const ctx = new AgentContext(
      this.client,
      claim,
      this.registry,
      allLocalTools,
      this.remoteTools,
      mcpToolInfos,
    );

    this.contexts.set(claim.executionId, ctx);

    try {
      const output = await this.process(ctx);
      await this.client.submitIntent({
        executionId: claim.executionId,
        sessionId: claim.sessionId,
        intentType: "complete",
        output,
      });
    } catch (err) {
      await this.tryFail(
        claim.executionId,
        claim.sessionId,
        err instanceof Error ? err.message : String(err),
      );
    } finally {
      this.contexts.delete(claim.executionId);
    }
  }

  private async tryFail(
    executionId: string,
    sessionId: string,
    error: string,
  ): Promise<void> {
    try {
      await this.client.submitIntent({
        executionId,
        sessionId,
        intentType: "fail",
        error,
      });
    } catch {
      // Best-effort
    }
  }

  private async initMcp(): Promise<void> {
    if (this.mcpConfigs.length === 0) return;
    if (this.mcpManager) return;

    const { McpManager: McpMgr } = await import("./mcp.js");
    this.mcpManager = new McpMgr();
    for (const config of this.mcpConfigs) {
      this.mcpManager.addServer(config.name, {
        command: config.command,
        args: config.args,
        env: config.env,
        url: config.url,
        headers: config.headers,
        prefix: config.prefix,
      });
    }
    await this.mcpManager.connectAll();
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
