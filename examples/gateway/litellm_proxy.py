"""LLM interceptor that fulfils the Rebuno step contract, as a litellm proxy callback.

No SDK needed — the contract is plain signed HTTP. Each intercepted call becomes an
`llm_call` step submitted under the dispatch the caller forwards; the kernel assigns
the step id and says whether to call the provider or return the recorded response.
Streamed and whole responses are both recorded, and either replays as either.

The agent forwards four headers per request:

    rebuno-execution-id   the execution the call belongs to
    rebuno-dispatch-id    the dispatch the agent is handling
    rebuno-agent-id       which agent this is
    rebuno-agent-secret   its signing secret
"""

import hashlib
import hmac
import json
import os

import httpx
from fastapi import HTTPException
from litellm import ModelResponse, stream_chunk_builder
from litellm.integrations.custom_logger import CustomLogger

REBUNO_URL = os.environ.get("REBUNO_URL", "http://localhost:8080")

_http = httpx.AsyncClient(base_url=REBUNO_URL.rstrip("/"), timeout=30)


async def _post(
    path: str, body: dict, agent_id: str, secret: str, extra: dict | None = None
) -> dict:
    """POST a signed body to the kernel's agent API."""
    raw = json.dumps(body).encode()
    headers = {
        "Content-Type": "application/json",
        "Rebuno-Agent-Id": agent_id,
        "Rebuno-Signature": "sha256="
        + hmac.new(secret.encode(), raw, hashlib.sha256).hexdigest(),
        **(extra or {}),
    }
    resp = await _http.post(path, content=raw, headers=headers)
    resp.raise_for_status()
    return resp.json() if resp.content else {}


class RebunoInterceptor(CustomLogger):
    """Runs each intercepted LLM call through the Rebuno step contract."""

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        headers = data["proxy_server_request"]["headers"]
        exec_id = headers.get("rebuno-execution-id")

        if not exec_id or not data.get("messages"):
            return data

        agent_id = headers.get("rebuno-agent-id")
        dispatch_id = headers.get("rebuno-dispatch-id")
        secret = headers.get("rebuno-agent-secret")

        if not (agent_id and secret and dispatch_id):
            raise HTTPException(
                status_code=400,
                detail={
                    "error": "rebuno gateway: missing rebuno-agent-id / -agent-secret / -dispatch-id"
                },
            )

        # call-identifying fields
        fields = ("model", "messages", "tools", "tool_choice")
        request = {k: data[k] for k in fields if data.get(k) is not None}

        dec = await _post(
            path=f"/v0/executions/{exec_id}/steps",
            body={
                "kind": "llm_call",
                "target": request.get("model", ""),
                "args": request,
                "idempotency": "safe_to_retry",
            },
            agent_id=agent_id,
            secret=secret,
            extra={"Rebuno-Dispatch-Id": dispatch_id},
        )
        if dec["decision"] not in ("proceed", "replay"):
            raise HTTPException(
                status_code=429 if dec["decision"] == "rate_limited" else 403,
                detail={
                    "error": f"rebuno gateway: {dec['decision']} {dec.get('reason', '')}".strip()
                },
            )

        if dec["decision"] == "replay":
            data["mock_response"] = ModelResponse(**dec["result"])
        else:
            data["metadata"]["rebuno_step_id"] = dec["step_id"]

        return data

    async def async_post_call_success_hook(self, data, user_api_key_dict, response):
        step_id = (data.get("metadata") or {}).get("rebuno_step_id")
        if step_id:
            body = (
                response.model_dump() if hasattr(response, "model_dump") else response
            )
            await self._record(
                data=data, step_id=step_id, outcome="complete", body={"result": body}
            )

    async def async_post_call_streaming_iterator_hook(
        self, user_api_key_dict, response, request_data
    ):
        """Where litellm sends a streamed call — the success hook never fires for one.
        Chunks go straight out to the caller as they arrive; the step is completed
        with the whole response assembled from them, the shape a replay expects.
        """
        step_id = (request_data.get("metadata") or {}).get("rebuno_step_id")
        chunks, error = [], None
        try:
            async for chunk in response:
                chunks.append(chunk)
                yield chunk
        except Exception as e:
            error = e
            raise
        finally:
            # in `finally`, not after the loop: a caller that disconnects mid-stream
            # closes this generator, and the step still has to be closed out — with
            # whatever arrived, the same as any other half-finished stream
            if step_id and error is not None:
                await self._record(
                    data=request_data,
                    step_id=step_id,
                    outcome="fail",
                    body={"error": {"message": str(error)}},
                )
            elif step_id:
                whole = stream_chunk_builder(chunks)
                await self._record(
                    data=request_data,
                    step_id=step_id,
                    outcome="complete",
                    body={"result": whole.model_dump() if whole else {}},
                )

    async def async_post_call_failure_hook(
        self, request_data, original_exception, user_api_key_dict, traceback_str=None
    ):
        step_id = (request_data.get("metadata") or {}).get("rebuno_step_id")
        if step_id:
            await self._record(
                data=request_data,
                step_id=step_id,
                outcome="fail",
                body={"error": {"message": str(original_exception)}},
            )

    async def _record(self, data: dict, step_id: str, outcome: str, body: dict) -> None:
        """Record the step's outcome."""
        headers = data["proxy_server_request"]["headers"]
        path = (
            f"/v0/executions/{headers['rebuno-execution-id']}/steps/{step_id}/{outcome}"
        )
        await _post(
            path=path,
            body=body,
            agent_id=headers["rebuno-agent-id"],
            secret=headers["rebuno-agent-secret"],
            extra={"Rebuno-Dispatch-Id": headers["rebuno-dispatch-id"]},
        )


interceptor = RebunoInterceptor()
