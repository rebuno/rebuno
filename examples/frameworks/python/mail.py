"""The mock `mail` tool that the agents call."""

import os

import httpx2
from rebuno import execution

MAIL_URL = os.environ.get("MAIL_URL")


async def send(to: str, body: str) -> dict:
    """Return a delivery receipt, posting the message to MAIL_URL first if that is set."""
    if not MAIL_URL:
        return {
            "status": "sent",
            "to": to,
            "simulated": True,
            "detail": f"simulated email to {to}; no delivery endpoint configured",
        }
    async with httpx2.AsyncClient() as http:
        response = await http.post(
            MAIL_URL,
            json={"to": to, "body": body},
            headers={"X-Execution-Id": str(execution().id)},
            timeout=30,
        )
        response.raise_for_status()
        return response.json()
