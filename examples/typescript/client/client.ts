// Interactive CLI client for creating executions and monitoring progress.
// Streams events in real-time, handles approval workflows, and displays results.

import { createInterface } from "node:readline/promises";
import { stdin, stdout, argv, exit } from "node:process";
import { RebunoClient, ExecutionStatus } from "rebuno";
import type { Event } from "rebuno";

const dim = (t: string) => `\x1b[90m${t}\x1b[0m`;
const bold = (t: string) => `\x1b[1m${t}\x1b[0m`;
const green = (t: string) => `\x1b[32m${t}\x1b[0m`;
const red = (t: string) => `\x1b[31m${t}\x1b[0m`;
const yellow = (t: string) => `\x1b[33m${t}\x1b[0m`;

const rl = createInterface({ input: stdin, output: stdout });

function prompt(message: string): Promise<string> {
  return rl.question(message);
}

async function selectAgent(client: RebunoClient): Promise<string> {
  const knownAgents: string[] = [];
  try {
    const data = await client.listExecutions({ limit: 100 });
    for (const ex of data.executions) {
      if (ex.agentId && !knownAgents.includes(ex.agentId)) {
        knownAgents.push(ex.agentId);
      }
    }
  } catch {}

  if (knownAgents.length === 0) {
    console.log(dim("  No agents found from previous executions."));
    while (true) {
      const agentId = (await prompt(`  ${bold("Agent ID")}: `)).trim();
      if (agentId) return agentId;
      console.log(red("  Agent ID cannot be empty."));
    }
  }

  console.log();
  for (let i = 0; i < knownAgents.length; i++) {
    console.log(`  ${dim(`[${i + 1}]`)} ${knownAgents[i]}`);
  }
  console.log(`  ${dim(`[${knownAgents.length + 1}]`)} Enter custom agent ID`);
  console.log();

  while (true) {
    const choice = (await prompt(`  ${bold("Select agent")}: `)).trim();
    if (/^\d+$/.test(choice)) {
      const idx = parseInt(choice, 10);
      if (idx >= 1 && idx <= knownAgents.length) return knownAgents[idx - 1];
      if (idx === knownAgents.length + 1) {
        while (true) {
          const agentId = (await prompt(`  ${bold("Agent ID")}: `)).trim();
          if (agentId) return agentId;
          console.log(red("  Agent ID cannot be empty."));
        }
      }
    }
    if (knownAgents.includes(choice)) return choice;
    console.log(red("  Invalid selection."));
  }
}

function printEvent(evt: Event): void {
  const seq = String(evt.sequence).padStart(3, "0");
  const ts = evt.timestamp ? String(evt.timestamp).split("T").pop()?.slice(0, 12) ?? "" : "";
  const parts = [dim(seq), dim(ts), evt.type];

  const payload = (evt.payload ?? {}) as Record<string, unknown>;

  if (evt.type === "step.created") {
    const tool = payload.tool_id ?? payload.toolId;
    if (tool) parts.push(dim(`tool=${tool}`));
  } else if (evt.type === "execution.completed") {
    const output = payload.output as Record<string, unknown> | undefined;
    const answer = output?.answer as string | undefined;
    if (answer) parts.push(dim(`(${answer.length} chars)`));
  } else if (evt.type === "execution.failed") {
    const error = (payload.error as string) ?? "";
    if (error) parts.push(red(error.slice(0, 80)));
  } else if (evt.type === "step.approval_required") {
    const tool = payload.tool_id ?? payload.toolId ?? "";
    const reason = (payload.reason as string) ?? "";
    parts.push(yellow(`tool=${tool}`));
    if (reason) parts.push(dim(reason));
  } else if (evt.type === "execution.blocked") {
    if (payload.reason === "approval") {
      const tool = payload.tool_id ?? payload.toolId ?? "";
      parts.push(yellow(`awaiting approval: ${tool}`));
    }
  }

  console.log(`  ${parts.join("  ")}`);
}

const TERMINAL_EVENTS = new Set([
  "execution.completed",
  "execution.failed",
  "execution.cancelled",
]);

async function runExecution(
  client: RebunoClient,
  agentId: string,
  query: string,
): Promise<void> {
  const result = await client.createExecution(agentId, { query });
  const executionId = result.id;
  console.log(`\n  ${dim("execution")} ${executionId}\n`);

  for await (const event of client.streamEvents(executionId)) {
    printEvent(event);
    if (TERMINAL_EVENTS.has(event.type)) break;

    const payload = (event.payload ?? {}) as Record<string, unknown>;
    if (event.type === "execution.blocked" && payload.reason === "approval") {
      const stepId = (payload.ref as string) ?? "";
      const toolId = payload.tool_id ?? payload.toolId ?? "";
      const args = payload.arguments;

      console.log();
      console.log(`  ${yellow(bold("Approval required"))}`);
      console.log(`  ${dim("tool:")} ${toolId}`);
      if (args) {
        const formatted =
          typeof args === "object"
            ? JSON.stringify(args, null, 2)
            : String(args);
        for (const line of formatted.split("\n")) {
          console.log(`  ${dim("  " + line)}`);
        }
      }
      console.log();

      let approved = false;
      while (true) {
        const choice = (
          await prompt(`  ${bold("Approve? [y/n]")}: `)
        )
          .trim()
          .toLowerCase();
        if (choice === "y" || choice === "yes") {
          approved = true;
          break;
        }
        if (choice === "n" || choice === "no") {
          approved = false;
          break;
        }
        console.log(red("  Please enter y or n."));
      }

      const signalPayload: Record<string, unknown> = {
        step_id: stepId,
        approved,
      };
      if (!approved) signalPayload.reason = "denied by user";

      try {
        await client.sendSignal(executionId, "step.approve", signalPayload);
      } catch (err) {
        console.log(
          `  ${red("Failed to send approval signal:")} ${err}`,
        );
      }
      console.log();
    }
  }

  const execution = await client.getExecution(executionId);

  console.log();
  if (
    execution.status === ExecutionStatus.Completed &&
    execution.output
  ) {
    const output = execution.output as Record<string, unknown>;
    const answer =
      typeof output === "object"
        ? (output.answer as string) ?? JSON.stringify(output)
        : String(output);

    console.log(`  ${green(bold("Result:"))}`);
    for (let line of answer.split("\n")) {
      while (line.length > 80) {
        let cut = line.slice(0, 80).lastIndexOf(" ");
        if (cut <= 0) cut = 80;
        console.log(`  ${line.slice(0, cut)}`);
        line = line.slice(cut).trimStart();
      }
      console.log(`  ${line}`);
    }
  } else if (execution.status === ExecutionStatus.Failed) {
    console.log(`  ${red(bold("Failed"))}`);
  } else if (execution.status === ExecutionStatus.Cancelled) {
    console.log(`  ${dim(bold("Cancelled"))}`);
  }
  console.log();
}

async function main() {
  let url = "http://localhost:8080";
  let apiKey = "";
  for (let i = 2; i < argv.length; i++) {
    if (argv[i] === "--url" && argv[i + 1]) url = argv[++i];
    if (argv[i] === "--api-key" && argv[i + 1]) apiKey = argv[++i];
  }

  const client = new RebunoClient({ baseUrl: url, apiKey });

  try {
    await client.health();
  } catch (err) {
    console.log(`\n  ${red("Cannot connect to kernel at")} ${url}`);
    console.log(`  ${dim(String(err))}\n`);
    exit(1);
  }

  console.log(`\n  ${bold("rebuno")} ${dim("client")}`);
  console.log(`  ${dim(url)}\n`);

  let agentId = await selectAgent(client);
  console.log(`\n  ${dim("agent")} ${bold(agentId)}\n`);

  while (true) {
    let query: string;
    try {
      query = (await prompt(`  ${bold("Query >")} `)).trim();
    } catch {
      console.log(`\n\n  ${dim("bye")}\n`);
      break;
    }

    if (!query) continue;
    if (["exit", "quit", ":q"].includes(query.toLowerCase())) {
      console.log(`\n  ${dim("bye")}\n`);
      break;
    }
    if (query.toLowerCase() === "/agent") {
      agentId = await selectAgent(client);
      console.log(`\n  ${dim("agent")} ${bold(agentId)}\n`);
      continue;
    }

    await runExecution(client, agentId, query);
  }

  rl.close();
}

main();
