# Setup

The SDK runs the agent; a separate kernel stores runs and dispatches webhooks.
Reuse an existing kernel/CLI, or download the latest
[release](https://github.com/rebuno/rebuno/releases) binary:

```bash
curl -sSfL https://github.com/rebuno/rebuno/releases/latest/download/rebuno_linux_amd64.tar.gz | tar xz
```

Match the archive to the platform (`linux`/`darwin`, `amd64`/`arm64`) and put
`rebuno` on `PATH`. Building from source instead needs Go 1.26+:
`go install github.com/rebuno/rebuno/cmd/rebuno@latest`.
The Python/npm SDK does not install this binary.

## Local configuration

Set these in both the kernel and agent terminals:

```bash
export REBUNO_URL=http://127.0.0.1:8080
export REBUNO_AGENT_SECRET=local-development-only
```

Agents use the shared HMAC secret. Clients/CLI use `REBUNO_API_KEY` for bearer
authentication against production kernels. Constructor options override SDK
environment settings.

Create `rebuno.yaml` for the language guides' starter:

```yaml
agents:
  - id: my-agent
    webhook_url: http://127.0.0.1:5000/webhook
    secret: ${REBUNO_AGENT_SECRET}
    policy: |
      default_action: deny
      rules:
        - id: allow-word-count
          when: { step_kind: tool_call, target: word_count }
          then: { decision: allow }
```

The loader expands environment variables; export the secret before loading.
Use managed secrets for deployment. For a separate bundle, replace `policy`
with `policy_file: policies/my-agent.yaml`, relative to the manifest.
Adapt permissions when adding tools or LLM calls.

## Run

Start the kernel, then the language guide's agent in another terminal:

```bash
rebuno dev --listen-addr 127.0.0.1:8080 --config rebuno.yaml
```

From a third terminal:

```bash
rebuno exec create my-agent '{"query":"hello world"}'
rebuno exec get <execution-id>
rebuno exec events <execution-id>
rebuno exec watch <execution-id>
```

`watch` waits for a terminal state. Dev mode disables client/admin bearer auth
but retains webhook signatures; restarting its in-memory kernel loses all runs.

For an existing kernel, `rebuno agent add rebuno.yaml` upserts registrations.
Check the selected kernel and agent before replacing one. Both URLs must be
reachable from their callers, including container networking and mount prefixes.
Hosting must keep handler work alive after the webhook response.

For production/Postgres configuration, consult
[deployment](https://github.com/rebuno/rebuno/blob/main/docs/deployment.md).
Other commands: [CLI](https://github.com/rebuno/rebuno/blob/main/docs/cli.md).
