export interface McpServerConfig {
  name: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  prefix?: string;
}

export interface McpToolInfo {
  id: string;
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

interface McpServerOptions {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  prefix?: string;
}

export class McpConnection {
  readonly name: string;
  readonly prefix: string;
  connected = false;

  private client: unknown;
  private options: McpServerOptions;

  constructor(name: string, prefix: string, options: McpServerOptions) {
    this.name = name;
    this.prefix = prefix;
    this.options = options;
  }

  async connect(): Promise<void> {
    const sdk: Record<string, unknown> = await import(
      /* @vite-ignore */ "@modelcontextprotocol/sdk/client/index.js" as string
    );
    const Client = sdk.Client as new (opts: Record<string, unknown>) => Record<string, (...args: unknown[]) => Promise<unknown>>;

    if (this.options.url) {
      const url = new URL(this.options.url);

      try {
        const httpMod: Record<string, unknown> = await import(
          /* @vite-ignore */ "@modelcontextprotocol/sdk/client/streamableHttp.js" as string
        );
        const StreamableHTTPClientTransport = httpMod.StreamableHTTPClientTransport as new (url: URL) => unknown;
        const client = new Client({ name: this.name, version: "1.0.0" });
        await client.connect(new StreamableHTTPClientTransport(url));
        this.client = client;
        this.connected = true;
        return;
      } catch {
        // Fall back to legacy SSE
      }

      try {
        const sseMod: Record<string, unknown> = await import(
          /* @vite-ignore */ "@modelcontextprotocol/sdk/client/sse.js" as string
        );
        const SSEClientTransport = sseMod.SSEClientTransport as new (url: URL) => unknown;
        const client = new Client({ name: this.name, version: "1.0.0" });
        await client.connect(new SSEClientTransport(url));
        this.client = client;
        this.connected = true;
        return;
      } catch (err) {
        throw new Error(
          `Failed to connect to MCP server '${this.name}' at ${this.options.url}: ${err instanceof Error ? err.message : String(err)}`,
        );
      }
    } else if (this.options.command) {
      const stdioMod: Record<string, unknown> = await import(
        /* @vite-ignore */ "@modelcontextprotocol/sdk/client/stdio.js" as string
      );
      const StdioClientTransport = stdioMod.StdioClientTransport as new (opts: Record<string, unknown>) => unknown;
      const client = new Client({ name: this.name, version: "1.0.0" });
      await client.connect(new StdioClientTransport({
        command: this.options.command,
        args: this.options.args ?? [],
        env: this.options.env,
      }));
      this.client = client;
      this.connected = true;
    } else {
      throw new Error(
        `MCP server '${this.name}' must specify either 'url' or 'command'`,
      );
    }
  }

  async disconnect(): Promise<void> {
    if (this.client) {
      try {
        await (this.client as { close: () => Promise<void> }).close();
      } catch {
        // Best-effort
      }
    }
    this.client = null;
    this.connected = false;
  }

  async listTools(): Promise<McpToolInfo[]> {
    if (!this.client) throw new Error(`MCP server '${this.name}' not connected`);

    const result = await (
      this.client as {
        listTools: () => Promise<{
          tools: Array<{
            name: string;
            description?: string;
            inputSchema?: Record<string, unknown>;
          }>;
        }>;
      }
    ).listTools();

    return result.tools.map((t) => ({
      id: `${this.prefix}.${t.name}`,
      name: t.name,
      description: t.description ?? "",
      inputSchema: t.inputSchema ?? {},
    }));
  }

  async callTool(
    toolName: string,
    args?: Record<string, unknown>,
  ): Promise<unknown> {
    if (!this.client) throw new Error(`MCP server '${this.name}' not connected`);

    const result = await (
      this.client as {
        callTool: (req: {
          name: string;
          arguments: Record<string, unknown>;
        }) => Promise<{
          content: Array<{ type: string; text?: string }>;
        }>;
      }
    ).callTool({ name: toolName, arguments: args ?? {} });

    const texts = result.content.map((item) =>
      item.type === "text" ? (item.text ?? "") : JSON.stringify(item),
    );
    return texts.length === 1 ? texts[0] : texts.join("\n");
  }
}

export class McpManager {
  private connections = new Map<string, McpConnection>();
  failed: Record<string, Error> = {};
  private retryTimer: ReturnType<typeof setInterval> | null = null;

  addServer(name: string, options: McpServerOptions): void {
    const prefix = options.prefix || name;
    this.connections.set(
      name,
      new McpConnection(name, prefix, options),
    );
  }

  addServersFromConfig(config: Record<string, unknown>): void {
    const servers = (config.mcpServers ?? config) as Record<
      string,
      Record<string, unknown>
    >;
    for (const [name, serverConfig] of Object.entries(servers)) {
      this.addServer(name, {
        command: serverConfig.command as string | undefined,
        args: serverConfig.args as string[] | undefined,
        env: serverConfig.env as Record<string, string> | undefined,
        url: serverConfig.url as string | undefined,
        headers: serverConfig.headers as Record<string, string> | undefined,
      });
    }
  }

  async connectAll(): Promise<void> {
    this.failed = {};

    for (const [name, conn] of this.connections) {
      try {
        await conn.connect();
      } catch (err) {
        this.failed[name] = err instanceof Error ? err : new Error(String(err));
      }
    }

    const connected = [...this.connections.values()].filter(
      (c) => c.connected,
    );
    if (connected.length === 0) {
      throw new Error(
        `All MCP servers failed to connect: ${JSON.stringify(
          Object.fromEntries(
            Object.entries(this.failed).map(([k, v]) => [k, v.message]),
          ),
        )}`,
      );
    }
  }

  async disconnectAll(): Promise<void> {
    if (this.retryTimer) {
      clearInterval(this.retryTimer);
      this.retryTimer = null;
    }

    for (const conn of this.connections.values()) {
      if (conn.connected) {
        try {
          await conn.disconnect();
        } catch {
          // Best-effort
        }
      }
    }
  }

  startRetryLoop(
    onReconnect?: () => Promise<void>,
    interval = 30000,
  ): void {
    if (this.retryTimer) return;

    this.retryTimer = setInterval(async () => {
      const reconnected: string[] = [];

      for (const name of Object.keys(this.failed)) {
        const conn = this.connections.get(name);
        if (!conn) continue;
        try {
          await conn.connect();
          reconnected.push(name);
        } catch {
          // Still failing
        }
      }

      for (const name of reconnected) {
        delete this.failed[name];
      }

      if (reconnected.length > 0 && onReconnect) {
        await onReconnect();
      }

      if (Object.keys(this.failed).length === 0 && this.retryTimer) {
        clearInterval(this.retryTimer);
        this.retryTimer = null;
      }
    }, interval);
  }

  async allTools(): Promise<McpToolInfo[]> {
    const tools: McpToolInfo[] = [];
    for (const conn of this.connections.values()) {
      if (conn.connected) {
        tools.push(...(await conn.listTools()));
      }
    }
    return tools;
  }

  async allToolIds(): Promise<string[]> {
    const tools = await this.allTools();
    return tools.map((t) => t.id);
  }

  hasTool(prefixedToolId: string): boolean {
    const dotIndex = prefixedToolId.indexOf(".");
    if (dotIndex === -1) return false;
    const prefix = prefixedToolId.slice(0, dotIndex);
    for (const conn of this.connections.values()) {
      if (conn.prefix === prefix && conn.connected) return true;
    }
    return false;
  }

  async callTool(
    prefixedToolId: string,
    args?: Record<string, unknown>,
  ): Promise<unknown> {
    const dotIndex = prefixedToolId.indexOf(".");
    if (dotIndex === -1) {
      throw new Error(
        `Invalid tool ID format: ${prefixedToolId} (expected 'prefix.tool_name')`,
      );
    }
    const prefix = prefixedToolId.slice(0, dotIndex);
    const toolName = prefixedToolId.slice(dotIndex + 1);

    for (const conn of this.connections.values()) {
      if (conn.prefix === prefix && conn.connected) {
        return conn.callTool(toolName, args);
      }
    }

    throw new Error(`No MCP connection found for prefix '${prefix}'`);
  }
}
