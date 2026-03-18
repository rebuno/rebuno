import { randomUUID } from "node:crypto";
import { RebunoClient } from "./client.js";
import { camelKeys } from "./internal/fetch.js";
import { jitteredBackoff } from "./internal/backoff.js";
import { parseSSEStream } from "./sse.js";
import { RebunoError } from "./errors.js";
import type { Job } from "./models.js";
import type { McpServerConfig, McpManager } from "./mcp.js";

export interface BaseRunnerOptions {
  runnerId: string;
  kernelUrl: string;
  capabilities?: string[];
  apiKey?: string;
  name?: string;
  reconnectDelay?: number;
  maxReconnectDelay?: number;
}

export abstract class BaseRunner {
  readonly runnerId: string;
  readonly name: string;
  readonly capabilities: string[];
  readonly consumerId: string;
  readonly reconnectDelay: number;
  readonly maxReconnectDelay: number;

  protected client: RebunoClient;

  private running = false;
  private abortController: AbortController | null = null;
  private mcpManager: McpManager | null = null;
  private mcpConfigs: McpServerConfig[] = [];

  constructor(options: BaseRunnerOptions) {
    if (!options.runnerId) throw new Error("runnerId must not be empty");
    if (!options.kernelUrl) throw new Error("kernelUrl must not be empty");

    this.runnerId = options.runnerId;
    this.name = options.name ?? options.runnerId;
    this.capabilities = options.capabilities ?? [];
    this.consumerId = `${options.runnerId}-${randomUUID().slice(0, 8)}`;
    this.reconnectDelay = options.reconnectDelay ?? 2.0;
    this.maxReconnectDelay = options.maxReconnectDelay ?? 60.0;

    this.client = new RebunoClient({
      baseUrl: options.kernelUrl,
      apiKey: options.apiKey,
    });
  }

  async execute(_toolId: string, _arguments: unknown): Promise<unknown> {
    throw new Error(
      `No handler for tool '${_toolId}'. Override execute() or add an MCP server.`,
    );
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

  private async mergedCapabilities(): Promise<string[]> {
    if (!this.mcpManager) return this.capabilities;
    const mcpToolIds = await this.mcpManager.allToolIds();
    return [...new Set([...this.capabilities, ...mcpToolIds])];
  }

  private async connectAndProcess(): Promise<void> {
    await this.initMcp();
    const allCaps = await this.mergedCapabilities();

    this.abortController = new AbortController();

    const params = new URLSearchParams({
      runner_id: this.runnerId,
      consumer_id: this.consumerId,
    });
    if (allCaps.length > 0) {
      params.set("capabilities", allCaps.join(","));
    }

    const resp = await this.client["fetchFn"](
      `${this.client.baseUrl}/v0/runners/stream?${params}`,
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

    if (this.mcpManager && Object.keys(this.mcpManager.failed).length > 0) {
      this.mcpManager.startRetryLoop(async () => {
        const newCaps = await this.mergedCapabilities();
        await this.client.updateCapabilities(this.runnerId, newCaps);
      });
    }

    for await (const event of parseSSEStream(resp.body)) {
      if (!this.running) return;
      if (event.type === "job.assigned") {
        const raw = JSON.parse(event.data);
        const args = raw.arguments;
        const job = camelKeys(raw) as unknown as Job;
        if (args !== undefined) job.arguments = args;
        this.handleJob(job).catch(() => {});
      }
    }
  }

  private async handleJob(job: Job): Promise<void> {
    try {
      await this.client.stepStarted(
        job.stepId,
        job.executionId,
        this.runnerId,
      );
    } catch {
      // Best-effort
    }

    try {
      let result: unknown;
      if (this.mcpManager?.hasTool(job.toolId)) {
        result = await this.mcpManager.callTool(
          job.toolId,
          job.arguments as Record<string, unknown>,
        );
      } else {
        result = await this.execute(job.toolId, job.arguments);
      }

      await this.client.submitResult({
        runnerId: this.runnerId,
        jobId: job.id,
        executionId: job.executionId,
        stepId: job.stepId,
        success: true,
        data: result,
      });
    } catch (err) {
      try {
        await this.client.submitResult({
          runnerId: this.runnerId,
          jobId: job.id,
          executionId: job.executionId,
          stepId: job.stepId,
          success: false,
          error: err instanceof Error ? err.message : String(err),
          retryable:
            (err as { retryable?: boolean }).retryable === true,
        });
      } catch {
        // Best-effort
      }
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
