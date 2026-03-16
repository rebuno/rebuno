from unittest.mock import AsyncMock, MagicMock

import pytest

from rebuno.async_client import AsyncRebunoClient
from rebuno.async_runner import AsyncBaseRunner
from rebuno.mcp import McpManager

from conftest import make_job


def _make_runner(**kwargs):
    """Helper to create a concrete AsyncBaseRunner subclass instance."""

    class TestRunner(AsyncBaseRunner):
        async def execute(self, tool_id, arguments):
            return {"result": "ok"}

    defaults = {"runner_id": "runner-1", "kernel_url": "http://localhost:8080"}
    defaults.update(kwargs)
    return TestRunner(**defaults)


def _mock_client():
    """Create a mock AsyncRebunoClient with async methods."""
    client = MagicMock(spec=AsyncRebunoClient)
    client.step_started = AsyncMock()
    client.submit_result = AsyncMock(return_value={"ok": True})
    client.close = AsyncMock()
    return client


class TestAsyncBaseRunnerInit:
    def test_empty_runner_id_raises(self):
        with pytest.raises(ValueError, match="runner_id must not be empty"):
            _make_runner(runner_id="")

    def test_empty_kernel_url_raises(self):
        with pytest.raises(ValueError, match="kernel_url must not be empty"):
            _make_runner(kernel_url="")


class TestAsyncBaseRunnerHandleJob:
    @pytest.mark.asyncio
    async def test_success(self):
        runner = _make_runner()
        runner._client = _mock_client()

        await runner._handle_job(make_job())

        runner._client.step_started.assert_called_once()
        runner._client.submit_result.assert_called_once()
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True
        assert call_kwargs["data"] == {"result": "ok"}

    @pytest.mark.asyncio
    async def test_execute_error_submits_failure(self):
        class FailRunner(AsyncBaseRunner):
            async def execute(self, tool_id, arguments):
                raise ValueError("unknown tool")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = _mock_client()

        await runner._handle_job(make_job())

        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is False
        assert "unknown tool" in call_kwargs["error"]

    @pytest.mark.asyncio
    async def test_retryable_error(self):
        class RetryableError(Exception):
            retryable = True

        class FailRunner(AsyncBaseRunner):
            async def execute(self, tool_id, arguments):
                raise RetryableError("temporary")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = _mock_client()

        await runner._handle_job(make_job())

        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["retryable"] is True

    @pytest.mark.asyncio
    async def test_step_started_failure_does_not_prevent_execution(self):
        runner = _make_runner()
        runner._client = _mock_client()
        runner._client.step_started = AsyncMock(side_effect=Exception("network error"))

        await runner._handle_job(make_job())

        runner._client.submit_result.assert_called_once()
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True

    @pytest.mark.asyncio
    async def test_submit_result_failure_on_error_path_is_swallowed(self):
        class FailRunner(AsyncBaseRunner):
            async def execute(self, tool_id, arguments):
                raise ValueError("tool broke")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = _mock_client()
        runner._client.submit_result = AsyncMock(side_effect=Exception("network down"))

        # Should not raise
        await runner._handle_job(make_job())


class TestAsyncBaseRunnerMcp:
    def test_mcp_server_registers_connection(self):
        runner = _make_runner()
        runner.mcp_server("fs", command="python", args=["server.py"])

        assert runner._mcp is not None
        assert "fs" in runner._mcp._connections

    @pytest.mark.asyncio
    async def test_handle_job_routes_to_mcp_when_tool_matches(self):
        class FailRunner(AsyncBaseRunner):
            async def execute(self, tool_id, arguments):
                raise RuntimeError("should not be called for MCP tools")

        runner = FailRunner(runner_id="runner-1", kernel_url="http://localhost:8080")
        runner._client = _mock_client()

        mock_mcp = AsyncMock(spec=McpManager)
        mock_mcp.call_tool = AsyncMock(return_value="mcp result")
        mock_mcp.has_tool = MagicMock(return_value=True)
        runner._mcp = mock_mcp

        await runner._handle_job(make_job(tool_id="fs.read_file", arguments=None))

        mock_mcp.call_tool.assert_called_once_with("fs.read_file", None)
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True
        assert call_kwargs["data"] == "mcp result"

    @pytest.mark.asyncio
    async def test_handle_job_falls_through_to_execute_when_mcp_no_match(self):
        runner = _make_runner()
        runner._client = _mock_client()

        mock_mcp = AsyncMock(spec=McpManager)
        mock_mcp.has_tool = MagicMock(return_value=False)
        runner._mcp = mock_mcp

        await runner._handle_job(make_job(tool_id="custom.tool"))

        mock_mcp.call_tool.assert_not_called()
        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is True
        assert call_kwargs["data"] == {"result": "ok"}

    @pytest.mark.asyncio
    async def test_handle_job_mcp_tool_error_submits_failure(self):
        runner = _make_runner()
        runner._client = _mock_client()

        mock_mcp = AsyncMock(spec=McpManager)
        mock_mcp.has_tool = MagicMock(return_value=True)
        mock_mcp.call_tool = AsyncMock(side_effect=RuntimeError("MCP server crashed"))
        runner._mcp = mock_mcp

        await runner._handle_job(make_job(tool_id="fs.read_file"))

        call_kwargs = runner._client.submit_result.call_args.kwargs
        assert call_kwargs["success"] is False
        assert "MCP server crashed" in call_kwargs["error"]

    @pytest.mark.asyncio
    async def test_merged_capabilities_without_mcp(self):
        runner = _make_runner(capabilities=["web.search"])
        caps = await runner._merged_capabilities()
        assert caps == ["web.search"]

    @pytest.mark.asyncio
    async def test_merged_capabilities_deduplicates(self):
        runner = _make_runner(capabilities=["web.search", "fs.read"])

        mock_mcp = AsyncMock(spec=McpManager)
        mock_mcp._connections = {"fs": MagicMock(connected=True)}
        mock_mcp.all_tool_ids = AsyncMock(return_value=["fs.read", "fs.write"])
        runner._mcp = mock_mcp

        caps = await runner._merged_capabilities()
        assert sorted(caps) == ["fs.read", "fs.write", "web.search"]

    def test_mcp_servers_from_config(self):
        runner = _make_runner()
        config = {
            "mcpServers": {
                "fs": {"command": "python", "args": ["server.py"]},
            }
        }
        runner.mcp_servers_from_config(config)
        assert "fs" in runner._mcp._connections
