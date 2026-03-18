# Deployment

## Production Mode

For persistent storage and authentication:

```bash
rebuno server \
  --production \
  --db-url "postgres://user:pass@localhost/rebuno" \
  --policy /path/to/your/policies \
  --bearer-token "your-secret-token"
```

Production mode requires `--policy`, `--db-url`, and `--bearer-token`. The kernel will refuse to start without them.

## Authentication
Required in production mode

```bash
# Set the token
rebuno server --bearer-token "your-secret-token" ...

# Or via environment variable
export REBUNO_BEARER_TOKEN="your-secret-token"
```

All API requests must include the token:

```bash
curl -X POST http://localhost:8080/v0/executions \
  -H "Authorization: Bearer your-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"agent_id": "researcher", "input": {"query": "hello"}}'
```

The Python SDK accepts it as `api_key`:

```python
agent = MyAgent(
    agent_id="researcher",
    kernel_url="http://localhost:8080",
    api_key="your-secret-token",
)
```

## Configuration Reference (`rebuno server`)

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--port` | `REBUNO_PORT` | `8080` | Kernel HTTP server port |
| `--bind` | `REBUNO_BIND` | `0.0.0.0` | Bind address |
| `--db-url` | `REBUNO_DB_URL` | | PostgreSQL connection string (required) |
| `--db-max-conns` | `REBUNO_DB_MAX_CONNS` | `0` | Database connection pool max size (0 = pgxpool default) |
| `--db-min-conns` | `REBUNO_DB_MIN_CONNS` | `0` | Database connection pool min size (0 = pgxpool default) |
| `--policy` | `REBUNO_POLICY` | | Path to policy YAML file or directory |
| `--production` | `REBUNO_PRODUCTION` | `false` | Enable production checks (requires policy + db + bearer token) |
| `--tls-cert` | `REBUNO_TLS_CERT` | | TLS certificate file path (optional) |
| `--tls-key` | `REBUNO_TLS_KEY` | | TLS key file path (optional) |
| `--execution-timeout` | `REBUNO_EXECUTION_TIMEOUT` | `1h` | Maximum wall-clock time for an execution |
| `--step-timeout` | `REBUNO_STEP_TIMEOUT` | `5m` | Default deadline for a single tool step |
| `--agent-timeout` | `REBUNO_AGENT_TIMEOUT` | `30s` | Agent connectivity timeout |
| `--log-level` | `REBUNO_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `--log-format` | `REBUNO_LOG_FORMAT` | `json` | Log format (json/text) |
| `--otel-endpoint` | `REBUNO_OTEL_ENDPOINT` | | OTLP gRPC endpoint for tracing |
| `--otel-sample-rate` | `REBUNO_OTEL_SAMPLE_RATE` | `0.1` | Trace sampling rate |
| `--retention-period` | `REBUNO_RETENTION_PERIOD` | `168h` | Retention period for terminal executions |
| `--cleanup-interval` | `REBUNO_CLEANUP_INTERVAL` | `1h` | Interval between cleanup sweeps |
| `--retry-base-delay` | `REBUNO_RETRY_BASE_DELAY` | `1s` | Base delay for step retries |
| `--retry-max-delay` | `REBUNO_RETRY_MAX_DELAY` | `30s` | Maximum delay for step retries |
| `--bearer-token` | `REBUNO_BEARER_TOKEN` | | Bearer token for API auth (optional in dev; required in production) |
| `--cors-origins` | `REBUNO_CORS_ORIGINS` | | Comma-separated CORS origins |
| `--redis-url` | `REBUNO_REDIS_URL` | | Redis URL for persistent job queue (optional) |
