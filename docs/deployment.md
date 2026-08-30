# Deployment

## Dev and server modes

`rebuno dev` runs entirely in memory with auth disabled. It is for local
development only, and nothing persists across restarts.

`rebuno server` is the production kernel. It requires Postgres and a bearer token
and refuses to start without them:

```bash
rebuno server \
  --db-url "postgres://USER:PASSWORD@localhost:5432/rebuno" \
  --bearer-token "$REBUNO_BEARER_TOKEN" \
  --config /etc/rebuno/agents.yaml
```

The schema is applied from embedded migrations on boot. The HTTP API is stateless,
so you scale by running more replicas behind a load balancer. Postgres is the only
coordination point, and singleton background work (approval expiry, execution
deadlines, cleanup) is leader-elected through a Postgres advisory lock.

## Authentication

All client and admin routes require the bearer token:

```bash
curl -X POST http://localhost:8080/v0/executions \
  -H "Authorization: Bearer $REBUNO_BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "researcher", "input": {"query": "hello"}}'
```

Both SDK clients read the token from `REBUNO_API_KEY` if you do not pass it
explicitly. Agent routes do not use the bearer token at all. They authenticate
with HMAC over the agent's registered secret. See
[Authentication](api.md#authentication).

## Provisioning agents

Register agents and their policies declaratively with `--config`, a manifest both
`dev` and `server` load on boot and upsert. See
[`examples/rebuno.dev.yaml`](../examples/rebuno.dev.yaml):

```yaml
agents:
  - id: researcher
    webhook_url: https://researcher.internal/webhook
    secret: ${RESEARCHER_SECRET}
    policy_file: policies/research.yaml   # or an inline `policy: |` block
```

`policy_file` paths resolve relative to the manifest. A malformed bundle fails the
boot, so a broken policy never falls back to permissive. You can also register
agents and load policy at runtime over the [admin API](api.md#admin-api).

## Configuration

Server flags and their environment-variable equivalents:

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--listen-addr` | `REBUNO_LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `--db-url` | `REBUNO_DB_URL` | none | Postgres URL. **Required** in server mode. |
| `--bearer-token` | `REBUNO_BEARER_TOKEN` | none | Client/admin API token. **Required** in server mode. |
| `--config` | none | none | Provisioning manifest path. |
| `--db-max-conns` | `REBUNO_DB_MAX_CONNS` | auto | Max DB pool connections. |
| `--db-min-conns` | `REBUNO_DB_MIN_CONNS` | auto | Min DB pool connections. |
| `--log-level` | `REBUNO_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |
| `--log-format` | `REBUNO_LOG_FORMAT` | `text` | `text` or `json`. |
| `--otel-endpoint` | `REBUNO_OTEL_ENDPOINT` | none | OTLP gRPC endpoint (empty turns tracing off). |
| `--otel-sample-rate` | `REBUNO_OTEL_SAMPLE_RATE` | `1.0` | Trace sampling rate. |
| `--otel-insecure` | `REBUNO_OTEL_INSECURE` | `false` | Plaintext OTLP connection. |

Additional environment-only settings:

| Env | Default | Description |
|-----|---------|-------------|
| `REBUNO_DISPATCH_MAX_ATTEMPTS` | `5` | Webhook delivery attempts before exhaustion. |
| `REBUNO_DISPATCH_TIMEOUT` | `30s` | Per-attempt webhook timeout. |
| `REBUNO_DISPATCH_CONCURRENCY` | `8` | Deliveries in flight per replica, and the most rows one claim takes. |
| `REBUNO_DISPATCH_LEASE_TIMEOUT` | `2m` | How long a claimed dispatch stays owned before the reaper reclaims it. Reclamation runs every two seconds, or four times per lease period if that is sooner. |
| `REBUNO_DEADLINE_TIMEOUT` | none | Max execution lifetime before auto-cancel. |
| `REBUNO_DEADLINE_CHECK_INTERVAL` | `30s` | How often expired executions are cancelled. Set to `0` to fold this into the `REBUNO_CLEANUP_INTERVAL` sweep. |
| `REBUNO_APPROVAL_TIMEOUT` | `15m` | Default time an approval can stay pending. On expiry the gated step is denied with `approval_timeout` and the execution resumes. |
| `REBUNO_CLEANUP_INTERVAL` | `10m` | How often the singleton worker runs approval expiry, deadline cancellation, and the retention sweep. |
| `REBUNO_RETENTION` | `24h` | How long terminal executions are kept. |
| `REBUNO_LEADER_LOCK_KEY` | `rebuno_scheduler_leader` | Advisory-lock key for leader election. |

## Docker

The image is built from [`deploy/Dockerfile`](../deploy/Dockerfile) and published
to `ghcr.io/rebuno/rebuno` on tagged releases. Its entrypoint is `rebuno server`,
so pass configuration as flags or `REBUNO_*` environment variables:

```bash
docker run -p 8080:8080 \
  -e REBUNO_DB_URL="postgres://…" \
  -e REBUNO_BEARER_TOKEN="…" \
  ghcr.io/rebuno/rebuno:latest
```

## Observability

- **Tracing** (OpenTelemetry): every API request and dispatch attempt, correlated
  by `execution_id` and `step_id`. Enable with `--otel-endpoint`.
- **Metrics** (Prometheus): scrape `/metrics` for execution counts by status,
  dispatch attempts, queue depth, approval wait times, replay-hit rate, and policy
  latency.
- **Logging**: structured, with `execution_id` and `step_id` correlation.
