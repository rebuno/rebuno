/**
 * Interactive CLI client for Rebuno.
 *
 * It creates an execution, streams the kernel's event log, and resolves any
 * human-in-the-loop approval the policy raises.
 */
import * as readline from "node:readline/promises";
import { exit, stdin, stdout } from "node:process";
import { parseArgs } from "node:util";

import { Client, type Event } from "rebuno";

const KNOWN_AGENTS = ["researcher", "shell", "mcp", "hello"];

const dim = (t: string) => `\x1b[90m${t}\x1b[0m`;
const bold = (t: string) => `\x1b[1m${t}\x1b[0m`;
const green = (t: string) => `\x1b[32m${t}\x1b[0m`;
const red = (t: string) => `\x1b[31m${t}\x1b[0m`;
const yellow = (t: string) => `\x1b[33m${t}\x1b[0m`;

const rl = readline.createInterface({ input: stdin, output: stdout });
const prompt = (text: string) => rl.question(text);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}

async function selectAgent(): Promise<string> {
  console.log();
  KNOWN_AGENTS.forEach((aid, i) => console.log(`  ${dim(`[${i + 1}]`)} ${aid}`));
  console.log(`  ${dim(`[${KNOWN_AGENTS.length + 1}]`)} Enter custom agent id`);
  console.log();
  while (true) {
    const choice = (await prompt(`  ${bold("Select agent")}: `)).trim();
    if (/^\d+$/.test(choice)) {
      const idx = Number(choice);
      if (idx >= 1 && idx <= KNOWN_AGENTS.length) return KNOWN_AGENTS[idx - 1]!;
      if (idx === KNOWN_AGENTS.length + 1) {
        while (true) {
          const aid = (await prompt(`  ${bold("Agent id")}: `)).trim();
          if (aid) return aid;
          console.log(red("  Agent id cannot be empty."));
        }
      }
    } else if (KNOWN_AGENTS.includes(choice)) {
      return choice;
    } else {
      console.log(red("  Invalid selection."));
    }
  }
}

function printEvent(evt: Event): void {
  const seq = String(evt.eventSeq).padStart(3, "0");
  const ts = evt.occurredAt ? (evt.occurredAt.split("T").at(-1) ?? "").slice(0, 12) : "";
  const parts = [dim(seq), dim(ts), evt.type];

  const payload = asObject(evt.payload);
  if (evt.type === "step.proposed") {
    if (payload.target) parts.push(dim(`tool=${payload.target}`));
  } else if (evt.type === "step.awaiting_approval") {
    parts.push(yellow(`tool=${payload.target ?? ""}`));
  } else if (evt.type === "execution.completed") {
    const out = asObject(payload.output);
    if (out.answer) parts.push(dim(`(${String(out.answer).length} chars)`));
  } else if (evt.type === "execution.failed") {
    if (payload.reason) parts.push(red(String(payload.reason).slice(0, 80)));
  }

  console.log(`  ${parts.join("  ")}`);
}

async function handleApproval(client: Client, approvalId: string, target: string): Promise<void> {
  console.log();
  console.log(`  ${yellow(bold("Approval required"))}  ${dim("tool:")} ${target || "?"}`);
  console.log();
  while (true) {
    const choice = (await prompt(`  ${bold("Approve? [y/n]")}: `)).trim().toLowerCase();
    if (choice === "y" || choice === "yes") {
      await client.grantApproval(approvalId, { decidedBy: "cli-user" });
      break;
    }
    if (choice === "n" || choice === "no") {
      await client.denyApproval(approvalId, { decidedBy: "cli-user", rationale: "denied by user" });
      break;
    }
    console.log(red("  Please enter y or n."));
  }
  console.log();
}

const TERMINAL = new Set(["execution.completed", "execution.failed", "execution.cancelled"]);

async function runExecution(client: Client, agentId: string, query: string): Promise<void> {
  const execution = await client.create(agentId, { query });
  console.log(`\n  ${dim("execution")} ${execution.id}\n`);

  let afterSeq = 0;
  let lastAwaitingTarget = "";
  while (true) {
    let events: Event[] | null = await client.events(execution.id, { afterSeq });
    for (const evt of events) {
      afterSeq = evt.eventSeq;
      printEvent(evt);
      if (evt.type === "step.awaiting_approval") {
        lastAwaitingTarget = String(asObject(evt.payload).target ?? "");
      } else if (evt.type === "approval.requested") {
        await handleApproval(client, String(asObject(evt.payload).approval_id ?? ""), lastAwaitingTarget);
      } else if (TERMINAL.has(evt.type)) {
        events = null;
        break;
      }
    }
    if (events === null) break;
    await sleep(500);
  }

  const final = await client.get(execution.id);
  console.log();
  if (final.status === "completed" && final.output != null) {
    const out = final.output;
    const answer = out && typeof out === "object" && "answer" in out ? (out as Record<string, unknown>).answer : out;
    console.log(`  ${green(bold("Result:"))}`);
    console.log(`  ${typeof answer === "string" ? answer : JSON.stringify(answer, null, 2)}`);
  } else if (final.status === "failed") {
    console.log(`  ${red(bold("Failed"))} ${dim(final.failureReason)}`);
  } else if (final.status === "cancelled") {
    console.log(`  ${dim(bold("Cancelled"))}`);
  }
  console.log();
}

async function main(): Promise<void> {
  const { values } = parseArgs({
    options: {
      url: { type: "string", default: "http://localhost:8080" },
      "api-key": { type: "string", default: "" },
      agent: { type: "string", default: "" },
    },
  });
  const url = values.url!;
  const client = new Client({ baseUrl: url, apiKey: values["api-key"] });

  console.log(`\n  ${bold("rebuno")} ${dim("client")}  ${dim(url)}`);
  let agentId = values.agent || (await selectAgent());
  console.log(`\n  ${dim("agent")} ${bold(agentId)}\n`);

  while (true) {
    let query: string;
    try {
      query = (await prompt(`  ${bold("Query >")} `)).trim();
    } catch {
      console.log(`\n\n  ${dim("bye")}\n`);
      return;
    }
    if (!query) continue;
    if (["exit", "quit", ":q"].includes(query.toLowerCase())) {
      console.log(`\n  ${dim("bye")}\n`);
      return;
    }
    if (query.toLowerCase() === "/agent") {
      agentId = await selectAgent();
      console.log(`\n  ${dim("agent")} ${bold(agentId)}\n`);
      continue;
    }
    try {
      await runExecution(client, agentId, query);
    } catch (e) {
      console.log(`\n  ${red("Error:")} ${e instanceof Error ? e.message : String(e)}\n`);
    }
  }
}

main()
  .catch((e) => {
    console.error(e);
    exit(1);
  })
  .finally(() => rl.close());
