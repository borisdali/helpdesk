# aiHelpDesk: demo O11y watcher / SRE boot app

The goal of this demo app is to showcase iHelpDesk
self diagnostic and troubleshooting funcionality.

## Architecture

If an upstream agent or an app doesn't tlak A2A natively,
the way to send requests to iHelpDesk is via the REST/gRPC gateway
The gateway is a stateless HTTP endpoint that translates POST
/api/v1/incidents into an A2A tool call.

The demo app consists of five sequential phases, all called
via the gateway, where the normal run only triggers the first two:
the agents discovery and the database health check.

All five phases can be also triggered with the '-force' flag.

## Phase descriptions

The phase descriptions are as follows:
  Phase 1 — Agent Discovery: GET /api/v1/agents to list available agents.
  Phase 2 — Health Check: POST /api/v1/db/check_connection with the connection string. If no anomaly keywords are found in the response, it reports "all clear" and exits (unless -force is set).
  Phase 3 — Deep Inspection: Calls get_database_stats and get_active_connections to gather more context.
  Phase 4 — Create Incident Bundle: Starts a callback HTTP server on :9090, then POST /api/v1/incidents with callback_url pointing back to itself.
  Phase 5 — Await Callback: Blocks until the incident agent's async callback arrives with the IncidentBundleResult payload, or times out after 120s.

## Sample Run

```
[boris@cassiopeia ~/cassiopeia/helpdesk]$ go run ./cmd/srebot/ -gateway http://localhost:8080 -conn "host=localhost port=15432 dbname=testdb user=postgres password=testpass"
[22:05:19] ── Phase 1: Agent Discovery ──────────────────────────
[22:05:19] GET /api/v1/agents
[22:05:19] Found 3 agents: postgres_database_agent, k8s_agent, incident_agent

[22:05:19] ── Phase 2: Health Check ─────────────────────────────
[22:05:19] POST /api/v1/db/check_connection
[22:05:22] Health check OK
[22:05:22] No anomalies — all clear.
```
