import { env } from "node:process";
import { defineTool, execution } from "rebuno";
import { z } from "zod";
import { mail } from "./mail.js";

async function request(operation: string, args: unknown) {
  if (!env.SUPPORT_URL) throw new Error("SUPPORT_URL is required");
  const response = await fetch(`${env.SUPPORT_URL.replace(/\/$/, "")}/${operation}`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Execution-Id": execution().id },
    body: JSON.stringify(args),
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) throw new Error(`Support endpoint returned HTTP ${response.status}`);
  return response.json();
}

const customer = z.object({ customer_id: z.string() });

function supportTool<S extends z.ZodType>(
  name: string, description: string, schema: S,
  idempotency: "safe_to_retry" | "at_most_once",
  execute: (args: z.output<S>) => Promise<unknown>,
) {
  return { name, description, schema, execute: defineTool({
    name, idempotency, execute: (args: unknown) => execute(schema.parse(args)),
  }) };
}

export const supportTools = [
  supportTool("lookup_customer", "Look up a customer's contact details.", customer,
    "safe_to_retry", (args) => request("customer", args)),
  supportTool("lookup_orders", "Look up a customer's recent orders.", customer,
    "safe_to_retry", (args) => request("orders", args)),
  supportTool("search_docs", "Search documentation for a support issue.", z.object({ query: z.string() }),
    "safe_to_retry", (args) => request("search", args)),
  supportTool("create_ticket", "Create one support ticket after investigating the issue.",
    z.object({ customer_id: z.string(), summary: z.string() }),
    "at_most_once", (args) => request("tickets", args)),
  supportTool("send_email", "Email the support summary after the kernel approves delivery.", z.object({ body: z.string() }),
    "at_most_once", (args) => mail.send("ops@acme.com", args.body)),
];
