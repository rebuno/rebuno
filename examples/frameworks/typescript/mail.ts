// The mock `mail` tool that the agents call.
import { env } from "node:process";
import { execution } from "rebuno";

const MAIL_URL = env.MAIL_URL;

export const mail = {
  /** Returns a delivery receipt, posting the message to MAIL_URL first if that is set. */
  async send(to: string, body: string) {
    if (!MAIL_URL) return { status: "sent", to, simulated: true, detail: `simulated email to ${to}; no delivery endpoint configured` };
    const resp = await fetch(MAIL_URL, {
      method: "POST",
      signal: AbortSignal.timeout(30_000),
      headers: { "content-type": "application/json", "X-Execution-Id": execution().id },
      body: JSON.stringify({ to, body }),
    });
    if (!resp.ok) throw new Error(`Mail endpoint returned HTTP ${resp.status}`);
    return resp.json();
  },
};
