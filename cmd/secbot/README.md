# aiHelpDesk: Security Responder Bot

The goal of this demo app is to showcase aiHelpDesk's
security monitoring and automated incident response functionality.

The demo is a security bot that monitors the real-time audit stream
for security-relevant events and automatically triggers incident bundle
creation when threats are detected. This demonstrates how aiHelpDesk
can be integrated into a security operations workflow where audit events
flow through the system and anomalies trigger automated responses.

## Architecture

```
Audit Socket → secbot → REST Gateway → incident_agent → Bundle
```

The secbot connects to the auditd Unix socket to receive real-time
audit events as they are recorded. When a security-relevant pattern
is detected, it calls the Gateway's incident API to create an
investigation bundle. This maintains architectural separation:
the security bot is an independent component that reacts to the
audit stream rather than being embedded in the agents themselves.

## Security Detection Patterns

The secbot watches for five types of security alerts:

| Alert Type | Description |
|------------|-------------|
| `high_volume` | Event rate exceeds threshold (default: 100/min) — may indicate attack or runaway process |
| `hash_mismatch` | Audit event hash doesn't verify — indicates tampering with audit trail |
| `unauthorized_destructive` | Destructive operation without approval — policy bypass attempt |
| `potential_sql_injection` | SQL syntax errors that may indicate injection attempts |
| `potential_command_injection` | Command errors suggesting shell injection attempts |

## Flow

The secbot operates in four phases:

```
Phase 1 — Startup: Parse flags, start callback server on :9091.
Phase 2 — Connect to Audit Stream: Dial the auditd Unix socket.
Phase 3 — Monitoring: Continuously process events, detect security patterns.
Phase 4 — Create Incident Bundle: When alert detected, POST /api/v1/incidents with callback URL.
```

Note that Phase 3 and 4 cycle repeatedly as alerts are detected.
A configurable cooldown (default: 5 minutes) prevents incident flooding.

## Command Line Flags

For details on how to run `secbot` in your specific deployment environment see [here](../../deploy/docker-compose/README.md#38-security-responder-secbot) for running via Docker containers, [here](../../deploy/host#77-security-responder-secbot) for running directly on a host and [here](../../deploy/helm/README.md#98-security-responder-secbot) for running on K8s.

```
-socket string
      Path to audit Unix socket (default "/tmp/helpdesk-audit.sock")
-gateway string
      Gateway base URL (default "http://localhost:8080")
-listen string
      Callback listener address (default ":9091")
-infra-key string
      Infrastructure identifier for incident bundles (default "security-incident")
-cooldown duration
      Minimum time between incident creations (default 5m)
-max-events-per-minute int
      Alert threshold for high-volume detection (default 100)
-dry-run
      Log alerts but don't create incidents
-verbose
      Log all received events
```

## Sample Run: Monitoring for Security Events

First, ensure the audit daemon and gateway are running:

```
# Terminal 1: Start auditd with socket enabled
go run ./cmd/auditd/ -socket /tmp/helpdesk-audit.sock

# Terminal 2: Start the gateway
go run ./cmd/gateway/

# Terminal 3: Start the database agent (to generate audit events)
HELPDESK_AUDIT_URL=http://localhost:1199 go run ./agents/database/
```

Then start the secbot in monitoring mode:

```
[boris@ ~/helpdesk]$ go run ./cmd/secbot/ -verbose

[14:32:01] ── Phase 1: Startup ──────────────────────────────────────
[14:32:01] Audit socket:  /tmp/helpdesk-audit.sock
[14:32:01] Gateway:       http://localhost:8080
[14:32:01] Callback:      :9091
[14:32:01] Cooldown:      5m0s
[14:32:01] Max events/min: 100
[14:32:01] Dry run:       false

[14:32:01] ── Phase 2: Connect to Audit Stream ──────────────────────
[14:32:01] Connected to audit stream

[14:32:01] ── Phase 3: Monitoring for Security Events ───────────────
[14:32:01] Watching for: high_volume, hash_mismatch, unauthorized_destructive, potential_sql_injection, potential_command_injection

[14:32:15] EVENT #1: evt_abc123 (type=tool_call)
[14:32:16] EVENT #2: evt_def456 (type=tool_result)
...
```

## Sample Run: Detecting Unauthorized Destructive Operation

To simulate a security alert, configure a policy that requires approval
for destructive operations, then trigger one without approval:

```
# policies.yaml
rules:
  - name: require-approval-for-destructive
    match:
      action_class: destructive
    effect: require_approval
```

When an agent attempts a destructive operation without approval,
the secbot detects it:

```
[14:35:22] SECURITY ALERT: unauthorized_destructive
[14:35:22]   Event ID:  evt_xyz789
[14:35:22]   Trace ID:  trace_abc
[14:35:22]   Time:      2026-02-16T14:35:22Z
[14:35:22]   Tool:      execute_sql
[14:35:22]   Agent:     postgres_database_agent

[14:35:22] ── Phase 4: Creating Security Incident Bundle ────────────
[14:35:22] POST /api/v1/incidents
[14:35:22]   infra_key:    security-incident
[14:35:22]   description:  Security alert: unauthorized_destructive (event: evt_x...
[14:35:22]   callback_url: http://192.168.1.151:9091/callback
[14:35:28] Incident creation initiated (744 chars response)

[14:35:28] ── Phase 3: Monitoring for Security Events ───────────────

[14:35:30] CALLBACK RECEIVED:
[14:35:30]   Incident ID: a1b2c3d4
[14:35:30]   Bundle:      /tmp/incident-a1b2c3d4-20260216-143522.tar.gz
[14:35:30]   Layers:      [os, storage]
```

## Dry Run Mode

Use `-dry-run` to test detection logic without creating incidents:

```
[boris@ ~/helpdesk]$ go run ./cmd/secbot/ -dry-run -max-events-per-minute 10

[14:40:01] ── Phase 3: Monitoring for Security Events ───────────────
[14:40:01] Watching for: high_volume, hash_mismatch, ...

[14:40:15] SECURITY ALERT: high_volume
[14:40:15]   Event ID:  evt_999
[14:40:15]   Trace ID:  trace_xyz
[14:40:15]   Time:      2026-02-16T14:40:15Z
[14:40:15]   [DRY RUN] Would create incident bundle
```

## Integration with AI Governance

The secbot is designed to work alongside the AI Governance framework:

1. **Policy Engine**: Defines which operations require approval
2. **Approval Workflows**: Human-in-the-loop for sensitive operations
3. **Audit Trail**: All events are recorded with hash chain integrity
4. **Secbot**: Monitors audit stream for policy violations and anomalies

This layered approach ensures that even if an agent attempts to bypass
policy controls, the security bot will detect and respond to the violation.
