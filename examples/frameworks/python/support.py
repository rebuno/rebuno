"""Support tools shared by the framework examples."""

import os

import httpx2
import mail
from rebuno import execution, tool


async def request(operation: str, **arguments) -> dict:
    url = os.environ["SUPPORT_URL"].rstrip("/")
    async with httpx2.AsyncClient() as client:
        response = await client.post(
            f"{url}/{operation}",
            json=arguments,
            headers={"X-Execution-Id": str(execution().id)},
            timeout=30,
        )
        response.raise_for_status()
        return response.json()


@tool("lookup_customer", idempotency="safe_to_retry")
async def lookup_customer(customer_id: str) -> dict:
    """Look up a customer's contact details."""
    return await request("customer", customer_id=customer_id)


@tool("lookup_orders", idempotency="safe_to_retry")
async def lookup_orders(customer_id: str) -> dict:
    """Look up a customer's recent orders."""
    return await request("orders", customer_id=customer_id)


@tool("search_docs", idempotency="safe_to_retry")
async def search_docs(query: str) -> dict:
    """Search documentation for a support issue."""
    return await request("search", query=query)


@tool("create_ticket", idempotency="at_most_once")
async def create_ticket(customer_id: str, summary: str) -> dict:
    """Create one support ticket after investigating the issue."""
    return await request("tickets", customer_id=customer_id, summary=summary)


@tool("send_email", idempotency="at_most_once")
async def send_email(body: str) -> dict:
    """Email the support summary after the kernel approves delivery."""
    return await mail.send("ops@acme.com", body)


TOOLS = [lookup_customer, lookup_orders, search_docs, create_ticket, send_email]
