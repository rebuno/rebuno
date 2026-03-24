from __future__ import annotations

import asyncio
import logging
from typing import Any

try:
    from fastmcp import Client
    from fastmcp.client.transports import StdioTransport, StreamableHttpTransport
except ImportError:
    Client = None  # type: ignore[assignment,misc]
    StdioTransport = None  # type: ignore[assignment,misc]
    StreamableHttpTransport = None  # type: ignore[assignment,misc]

logger = logging.getLogger("rebuno.mcp")


class McpConnection:
    """Wraps a single FastMCP Client connection to an MCP server."""

    def __init__(
        self,
        name: str,
        prefix: str,
        client: Any,
    ):
        self.name = name
        self.prefix = prefix
        self._client = client
        self.connected = False
        self._ctx: Any = None

    async def connect(self) -> None:
        self._ctx = await self._client.__aenter__()
        self.connected = True
        logger.info("MCP server connected: %s", self.name)

    async def disconnect(self) -> None:
        if self._ctx is not None:
            await self._client.__aexit__(None, None, None)
            self._ctx = None
            self.connected = False
            logger.info("MCP server disconnected: %s", self.name)

    async def list_tools(self) -> list[dict[str, Any]]:
        raw_tools = await self._client.list_tools()
        return [
            {
                "id": f"{self.prefix}.{t.name}",
                "name": t.name,
                "description": getattr(t, "description", "") or "",
                "input_schema": getattr(t, "inputSchema", {}),
            }
            for t in raw_tools
        ]

    async def call_tool(self, tool_name: str, arguments: dict[str, Any] | None = None) -> Any:
        result = await self._client.call_tool(tool_name, arguments or {})
        texts = [
            item.text if getattr(item, "type", None) == "text" else str(item)
            for item in result.content
        ]
        if len(texts) == 1:
            return texts[0]
        return "\n".join(texts)


class McpManager:
    """Manages multiple MCP server connections with partial failure tolerance."""

    def __init__(self) -> None:
        self._connections: dict[str, McpConnection] = {}
        self._failed: dict[str, Exception] = {}
        self._retry_task: asyncio.Task[None] | None = None

    def add_server(
        self,
        name: str,
        *,
        command: str = "",
        args: list[str] | None = None,
        env: dict[str, str] | None = None,
        url: str = "",
        headers: dict[str, str] | None = None,
        prefix: str = "",
    ) -> None:
        if Client is None:
            raise ImportError(
                "fastmcp is required for MCP support. Install it with: pip install rebuno[mcp]"
            )

        if not prefix:
            prefix = name

        if url:
            transport = StreamableHttpTransport(url=url, headers=headers) if headers else url
            client = Client(transport)
        elif command:
            transport = StdioTransport(
                command=command,
                args=args or [],
                env=env,
            )
            client = Client(transport)
        else:
            raise ValueError(f"Server '{name}' must specify either 'url' or 'command'")

        self._connections[name] = McpConnection(name=name, prefix=prefix, client=client)

    def add_servers_from_config(self, config: dict[str, Any]) -> None:
        servers = config.get("mcpServers", config)
        for name, server_config in servers.items():
            if "url" in server_config:
                self.add_server(
                    name,
                    url=server_config["url"],
                    headers=server_config.get("headers"),
                )
            elif "command" in server_config:
                self.add_server(
                    name,
                    command=server_config["command"],
                    args=server_config.get("args"),
                    env=server_config.get("env"),
                )
            else:
                raise ValueError(f"Server '{name}' must have 'url' or 'command'")

    async def connect_all(self) -> None:
        self._failed.clear()
        for name, conn in self._connections.items():
            try:
                await conn.connect()
            except Exception as e:
                logger.warning("MCP server '%s' failed to connect: %s", name, e)
                self._failed[name] = e

        connected = [n for n, c in self._connections.items() if c.connected]
        if not connected:
            raise RuntimeError(
                f"All MCP servers failed to connect: {self._failed}"
            )

        logger.info(
            "MCP connections ready: %d connected, %d failed",
            len(connected),
            len(self._failed),
        )

    async def disconnect_all(self) -> None:
        if self._retry_task and not self._retry_task.done():
            self._retry_task.cancel()
            try:
                await self._retry_task
            except asyncio.CancelledError:
                pass

        for conn in self._connections.values():
            if conn.connected:
                try:
                    await conn.disconnect()
                except Exception:
                    logger.debug("Error disconnecting %s", conn.name, exc_info=True)

    def start_retry_loop(self, on_reconnect: Any = None, interval: float = 30.0) -> None:
        if self._retry_task and not self._retry_task.done():
            return
        self._retry_task = asyncio.create_task(
            self._retry_failed(on_reconnect, interval)
        )

    async def _retry_failed(self, on_reconnect: Any, interval: float) -> None:
        while self._failed:
            await asyncio.sleep(interval)
            reconnected = []
            for name in list(self._failed):
                conn = self._connections.get(name)
                if conn is None:
                    continue
                try:
                    await conn.connect()
                    reconnected.append(name)
                    logger.info("MCP server '%s' reconnected", name)
                except Exception:
                    logger.debug("Retry failed for '%s'", name, exc_info=True)

            for name in reconnected:
                del self._failed[name]

            if reconnected and on_reconnect:
                await on_reconnect()

    async def all_tools(self) -> list[dict[str, Any]]:
        tools = []
        for conn in self._connections.values():
            if conn.connected:
                tools.extend(await conn.list_tools())
        return tools

    async def all_tool_ids(self) -> list[str]:
        tools = await self.all_tools()
        return [t["id"] for t in tools]

    def _parse_tool_id(self, prefixed_tool_id: str) -> tuple[str, str]:
        parts = prefixed_tool_id.split(".", 1)
        if len(parts) != 2:
            raise ValueError(f"Invalid tool ID format: {prefixed_tool_id} (expected 'prefix.tool_name')")
        return parts[0], parts[1]

    def _find_connection(self, prefix: str) -> McpConnection | None:
        for conn in self._connections.values():
            if conn.prefix == prefix and conn.connected:
                return conn
        return None

    def has_tool(self, prefixed_tool_id: str) -> bool:
        try:
            prefix, _ = self._parse_tool_id(prefixed_tool_id)
        except ValueError:
            return False
        return self._find_connection(prefix) is not None

    async def call_tool(self, prefixed_tool_id: str, arguments: dict[str, Any] | None = None) -> Any:
        prefix, tool_name = self._parse_tool_id(prefixed_tool_id)
        conn = self._find_connection(prefix)
        if conn is None:
            raise ValueError(f"No MCP connection found for prefix '{prefix}'")
        return await conn.call_tool(tool_name, arguments)
