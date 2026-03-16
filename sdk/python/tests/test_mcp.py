import asyncio
from unittest.mock import AsyncMock, MagicMock

import pytest

from rebuno.mcp import McpConnection, McpManager


class TestMcpConnection:
    @pytest.mark.asyncio
    async def test_list_tools_prefixed(self):
        mock_tool = MagicMock()
        mock_tool.name = "read_file"
        mock_tool.description = "Read a file"
        mock_tool.inputSchema = {"type": "object", "properties": {"path": {"type": "string"}}}

        mock_client = AsyncMock()
        mock_client.list_tools.return_value = [mock_tool]
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=False)

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        await conn.connect()
        tools = await conn.list_tools()

        assert len(tools) == 1
        assert tools[0]["id"] == "fs.read_file"
        assert tools[0]["name"] == "read_file"
        assert tools[0]["description"] == "Read a file"

    @pytest.mark.asyncio
    async def test_list_tools_missing_description_defaults_to_empty(self):
        mock_tool = MagicMock(spec=[])  # no attributes by default
        mock_tool.name = "do_thing"

        mock_client = AsyncMock()
        mock_client.list_tools.return_value = [mock_tool]
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=False)

        conn = McpConnection(name="x", prefix="x", client=mock_client)
        await conn.connect()
        tools = await conn.list_tools()

        assert tools[0]["description"] == ""
        assert tools[0]["input_schema"] == {}

    @pytest.mark.asyncio
    async def test_call_tool_single_text_content(self):
        mock_result = MagicMock()
        mock_result.content = [MagicMock(text="file contents")]
        mock_result.content[0].type = "text"

        mock_client = AsyncMock()
        mock_client.call_tool.return_value = mock_result

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        result = await conn.call_tool("read_file", {"path": "/tmp/test"})

        mock_client.call_tool.assert_called_once_with("read_file", {"path": "/tmp/test"})
        assert result == "file contents"

    @pytest.mark.asyncio
    async def test_call_tool_multiple_content_joined(self):
        item1 = MagicMock()
        item1.type = "text"
        item1.text = "part1"
        item2 = MagicMock()
        item2.type = "text"
        item2.text = "part2"
        mock_result = MagicMock()
        mock_result.content = [item1, item2]

        mock_client = AsyncMock()
        mock_client.call_tool.return_value = mock_result

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        result = await conn.call_tool("read_file", {})

        assert result == "part1\npart2"

    @pytest.mark.asyncio
    async def test_call_tool_none_arguments_becomes_empty_dict(self):
        mock_result = MagicMock()
        mock_result.content = [MagicMock(text="ok")]
        mock_result.content[0].type = "text"

        mock_client = AsyncMock()
        mock_client.call_tool.return_value = mock_result

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        await conn.call_tool("some_tool", None)

        mock_client.call_tool.assert_called_once_with("some_tool", {})

    @pytest.mark.asyncio
    async def test_call_tool_non_text_content_uses_str(self):
        item = MagicMock()
        item.type = "image"
        # No .text attribute, should fall through to str(item)

        mock_result = MagicMock()
        mock_result.content = [item]

        mock_client = AsyncMock()
        mock_client.call_tool.return_value = mock_result

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        result = await conn.call_tool("tool", {})

        assert isinstance(result, str)

    @pytest.mark.asyncio
    async def test_connect_and_disconnect_lifecycle(self):
        mock_client = AsyncMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=False)

        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        assert conn.connected is False

        await conn.connect()
        assert conn.connected is True

        await conn.disconnect()
        assert conn.connected is False
        assert conn._ctx is None
        mock_client.__aexit__.assert_called_once()

    @pytest.mark.asyncio
    async def test_disconnect_when_not_connected_is_noop(self):
        mock_client = AsyncMock()
        conn = McpConnection(name="fs", prefix="fs", client=mock_client)
        # Should not raise
        await conn.disconnect()
        assert conn.connected is False


class TestMcpManager:
    def test_add_server_with_command(self):
        mgr = McpManager()
        mgr.add_server("fs", command="python", args=["server.py"])
        assert "fs" in mgr._connections
        assert mgr._connections["fs"].prefix == "fs"

    def test_add_server_with_url(self):
        mgr = McpManager()
        mgr.add_server("remote", url="http://localhost:9000/mcp")
        assert "remote" in mgr._connections

    def test_add_server_custom_prefix(self):
        mgr = McpManager()
        mgr.add_server("filesystem", command="python", args=["s.py"], prefix="fs")
        assert mgr._connections["filesystem"].prefix == "fs"

    def test_add_server_no_url_or_command_raises(self):
        mgr = McpManager()
        with pytest.raises(ValueError, match="must specify either 'url' or 'command'"):
            mgr.add_server("bad")

    @pytest.mark.asyncio
    async def test_connect_all_partial_failure(self):
        good_conn = AsyncMock(spec=McpConnection)
        good_conn.name = "good"
        good_conn.prefix = "good"
        good_conn.connect = AsyncMock()
        good_conn.connected = True

        bad_conn = AsyncMock(spec=McpConnection)
        bad_conn.name = "bad"
        bad_conn.prefix = "bad"
        bad_conn.connect = AsyncMock(side_effect=Exception("connection refused"))
        bad_conn.connected = False

        mgr = McpManager()
        mgr._connections = {"good": good_conn, "bad": bad_conn}

        await mgr.connect_all()

        assert "bad" in mgr._failed
        assert "good" not in mgr._failed

    @pytest.mark.asyncio
    async def test_connect_all_total_failure_raises(self):
        bad_conn = AsyncMock(spec=McpConnection)
        bad_conn.name = "bad"
        bad_conn.prefix = "bad"
        bad_conn.connect = AsyncMock(side_effect=Exception("refused"))
        bad_conn.connected = False

        mgr = McpManager()
        mgr._connections = {"bad": bad_conn}

        with pytest.raises(RuntimeError, match="All MCP servers failed"):
            await mgr.connect_all()

    @pytest.mark.asyncio
    async def test_all_tools_aggregates_connected_servers(self):
        conn1 = AsyncMock(spec=McpConnection)
        conn1.connected = True
        conn1.list_tools.return_value = [
            {"id": "fs.read", "name": "read", "description": "Read"},
        ]

        conn2 = AsyncMock(spec=McpConnection)
        conn2.connected = True
        conn2.list_tools.return_value = [
            {"id": "git.status", "name": "status", "description": "Status"},
        ]

        mgr = McpManager()
        mgr._connections = {"fs": conn1, "git": conn2}

        tools = await mgr.all_tools()
        ids = [t["id"] for t in tools]
        assert "fs.read" in ids
        assert "git.status" in ids

    @pytest.mark.asyncio
    async def test_all_tools_skips_disconnected_servers(self):
        connected = AsyncMock(spec=McpConnection)
        connected.connected = True
        connected.list_tools.return_value = [
            {"id": "fs.read", "name": "read", "description": "Read"},
        ]

        disconnected = AsyncMock(spec=McpConnection)
        disconnected.connected = False

        mgr = McpManager()
        mgr._connections = {"fs": connected, "bad": disconnected}

        tools = await mgr.all_tools()
        assert len(tools) == 1
        disconnected.list_tools.assert_not_called()

    @pytest.mark.asyncio
    async def test_call_tool_routes_by_prefix(self):
        conn = AsyncMock(spec=McpConnection)
        conn.prefix = "fs"
        conn.connected = True
        conn.call_tool.return_value = "result"

        mgr = McpManager()
        mgr._connections = {"fs": conn}

        result = await mgr.call_tool("fs.read_file", {"path": "/tmp"})

        conn.call_tool.assert_called_once_with("read_file", {"path": "/tmp"})
        assert result == "result"

    @pytest.mark.asyncio
    async def test_call_tool_unknown_prefix_raises(self):
        mgr = McpManager()
        mgr._connections = {}

        with pytest.raises(ValueError, match="No MCP connection"):
            await mgr.call_tool("unknown.tool", {})

    def test_parse_tool_id_no_dot_raises(self):
        mgr = McpManager()
        with pytest.raises(ValueError, match="Invalid tool ID format"):
            mgr._parse_tool_id("nodot")

    def test_parse_tool_id_with_multiple_dots(self):
        mgr = McpManager()
        prefix, name = mgr._parse_tool_id("ns.sub.tool")
        assert prefix == "ns"
        assert name == "sub.tool"

    def test_has_tool_returns_true_for_connected_prefix(self):
        conn = MagicMock(spec=McpConnection)
        conn.prefix = "fs"
        conn.connected = True

        mgr = McpManager()
        mgr._connections = {"fs": conn}

        assert mgr.has_tool("fs.read_file") is True

    def test_has_tool_returns_false_for_disconnected_prefix(self):
        conn = MagicMock(spec=McpConnection)
        conn.prefix = "fs"
        conn.connected = False

        mgr = McpManager()
        mgr._connections = {"fs": conn}

        assert mgr.has_tool("fs.read_file") is False

    def test_has_tool_returns_false_for_unparseable_id(self):
        mgr = McpManager()
        assert mgr.has_tool("nodot") is False

    def test_add_servers_from_config_with_mcpservers_key(self):
        mgr = McpManager()
        config = {
            "mcpServers": {
                "fs": {"command": "python", "args": ["server.py"]},
                "remote": {"url": "http://localhost:9000"},
            }
        }
        mgr.add_servers_from_config(config)
        assert "fs" in mgr._connections
        assert "remote" in mgr._connections

    def test_add_servers_from_config_direct_mapping(self):
        mgr = McpManager()
        config = {
            "fs": {"command": "python", "args": ["server.py"]},
        }
        mgr.add_servers_from_config(config)
        assert "fs" in mgr._connections

    def test_add_servers_from_config_missing_url_and_command_raises(self):
        mgr = McpManager()
        with pytest.raises(ValueError, match="must have 'url' or 'command'"):
            mgr.add_servers_from_config({"bad": {"headers": {}}})

    @pytest.mark.asyncio
    async def test_disconnect_all(self):
        conn1 = AsyncMock(spec=McpConnection)
        conn1.connected = True
        conn1.disconnect = AsyncMock()

        conn2 = AsyncMock(spec=McpConnection)
        conn2.connected = False
        conn2.disconnect = AsyncMock()

        mgr = McpManager()
        mgr._connections = {"a": conn1, "b": conn2}

        await mgr.disconnect_all()

        conn1.disconnect.assert_called_once()
        conn2.disconnect.assert_not_called()

    @pytest.mark.asyncio
    async def test_disconnect_all_cancels_retry_task(self):
        mgr = McpManager()
        mgr._connections = {}

        # Create a real task that we can cancel
        async def _noop():
            await asyncio.sleep(999)

        task = asyncio.create_task(_noop())
        mgr._retry_task = task

        await mgr.disconnect_all()

        assert task.cancelled()

    @pytest.mark.asyncio
    async def test_call_tool_disconnected_prefix_raises(self):
        conn = AsyncMock(spec=McpConnection)
        conn.prefix = "fs"
        conn.connected = False

        mgr = McpManager()
        mgr._connections = {"fs": conn}

        with pytest.raises(ValueError, match="No MCP connection found for prefix"):
            await mgr.call_tool("fs.read_file", {})
