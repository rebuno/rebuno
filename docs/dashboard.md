# Dashboard

The dashboard is a Next.js app that reads and drives a running kernel over the
[HTTP API](api.md). It lives in its own repository,
[rebuno/dashboard](https://github.com/rebuno/dashboard).

## Pages

| Page | What it does |
|------|--------------|
| Executions | Lists executions and opens one to show its event log and steps side by side. Creates a new execution against any registered agent, or cancels a running one. |
| Agents | Lists registered agents, registers a new one, deletes one, and edits its policy bundle. |
| Approvals | Lists approvals still pending, soonest timeout first, and grants or denies each one. |
| Metrics | Kernel counters and latency quantiles: executions, steps, dispatch outcomes, policy decisions, replay hits, and queue depth. |

## Reaching the kernel

The browser never calls the kernel. Every request goes to the dashboard's own
`/api/v0/*` route. That route forwards it to `REBUNO_URL` and attaches
`Authorization: Bearer $REBUNO_API_KEY`. The token never reaches the browser.

Give it a token with the access you want the dashboard to have. Anyone who can
reach the dashboard gets that access. Put it behind your own authentication
before exposing it.

## Metrics

Set `PROMETHEUS_URL` and the metrics page queries Prometheus over a time range
you pick on the page.

With it unset the page scrapes the kernel's `/metrics` endpoint instead. Those
are one process's in-memory counters. They are cumulative since that replica
started, so no time range applies. Across replicas you see whichever one
answered. The page labels which source it used. Point a real Prometheus at the
kernel for anything beyond local development.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `REBUNO_URL` | `http://localhost:8080` | Kernel base URL. |
| `REBUNO_API_KEY` | none | Bearer token for the kernel's client and admin routes. Required against a kernel started with `rebuno server`. |
| `PROMETHEUS_URL` | none | Prometheus base URL for the metrics page. |
| `PORT` | `3000` | Listen port. |
| `HOSTNAME` | `0.0.0.0` | Listen address. |

## Running it

```bash
pnpm install
pnpm dev
```

Needs Node 20.9 or later and a kernel to talk to. `rebuno dev` is enough for
local work. It runs with auth disabled, so it needs no `REBUNO_API_KEY`.

## Docker

The image is built from
[`deploy/Dockerfile`](https://github.com/rebuno/dashboard/blob/main/deploy/Dockerfile)
and published to `ghcr.io/rebuno/dashboard` on tagged releases. It serves a
standalone Next.js build on port 3000:

```bash
docker run -p 3000:3000 \
  -e REBUNO_URL="http://kernel:8080" \
  -e REBUNO_API_KEY="$REBUNO_BEARER_TOKEN" \
  ghcr.io/rebuno/dashboard:latest
```
