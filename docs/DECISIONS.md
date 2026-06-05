# aiHelpDesk Decision Hub

The Decision Hub is a unified surface that aggregates every pending human decision across all aiHelpDesk subsystems. [Playbook](PLAYBOOKS.md) gates, [Fleet](FLEET.md) approvals and per-step [agent approvals](PLAYBOOKS.md#approval-modes). All of that goes into a single list with a single resolve endpoint. Operators get one place to look. Webhooks and email fire for all types.

---

## Decision types

| Type | Prefix | Raised by | Raised when |
|---|---|---|---|
| `gate` | `gate:{runID}` | Gateway | Triage playbook completes and `gate_escalation=true`; `TRANSITION_TO` or `ESCALATE_TO` signal present and `recommended` is actionable |
| `fleet_approval` | `fleet:{approvalID}` | fleet-runner | Job contains write or destructive steps and requires top-level approval |
| `step_approval` | `step:{approvalID}` | Gateway | `agent_approve` playbook reaches a write/destructive step |

Gates have two sub-types, reflected in `extra.gate_type`:

| `gate_type` | Signal | What it means |
|---|---|---|
| `transition` | `TRANSITION_TO:` | Triage handing off to its expected remediation counterpart within the same problem domain (e.g. `pbs_vacuum_triage` → `pbs_vacuum_remediate`). Routine pipeline step. |
| `escalation` | `ESCALATE_TO:` | True cross-domain handoff to a different agent or domain (e.g. DB agent → SysAdmin agent). May warrant closer operator scrutiny. |

---

## Listing pending decisions

```
GET /api/v1/decisions
```

Returns all pending decisions across all types, sorted newest-first.

**Query parameters:**

| Parameter | Default | Description |
|---|---|---|
| `status` | `pending` | Filter by status: `pending`, `approved`, `denied`, `expired`, `abandoned` |
| `type` | _(all)_ | Filter by type: `gate`, `fleet_approval`, `step_approval` |
| `limit` | `50` | Maximum results |

**Example:**

```bash
curl http://localhost:8080/api/v1/decisions | jq .
```

```json
{
  "decisions": [
    {
      "id":           "gate:plr_a3f7c1b2",
      "type":         "gate",
      "status":       "pending",
      "summary":      "Triage complete — TRANSITION_TO pbs_vacuum_remediate",
      "requested_by": "alice",
      "requested_at": "2026-06-01T14:23:01Z",
      "resolve_url":  "POST https://helpdesk.internal/api/v1/decisions/gate:plr_a3f7c1b2/resolve",
      "extra": {
        "gate_type":         "transition",
        "transition_target": "pbs_vacuum_remediate",
        "findings":          "Table public.orders has 94% dead tuple ratio...",
        "series_id":         "pbs_vacuum_triage"
      }
    },
    {
      "id":           "gate:plr_b8e2d4f1",
      "type":         "gate",
      "status":       "pending",
      "summary":      "Triage complete — ESCALATE_TO pbs_sysadmin_docker_inspect",
      "requested_by": "bob",
      "requested_at": "2026-06-01T13:11:45Z",
      "resolve_url":  "POST https://helpdesk.internal/api/v1/decisions/gate:plr_b8e2d4f1/resolve",
      "extra": {
        "gate_type":          "escalation",
        "escalation_target":  "pbs_sysadmin_docker_inspect",
        "findings":           "Connection refused — Docker-level investigation needed.",
        "series_id":          "pbs_connection_triage",
        "confidence_warning": "Primary hypothesis confidence 55%"
      }
    }
  ],
  "total": 2
}
```

---

## Resolving a decision

```
POST /api/v1/decisions/{id}/resolve
```

The `{id}` prefix determines routing:

- `gate:{runID}` → calls the playbook `proceed-escalation` endpoint internally
- `fleet:{approvalID}` → patches the fleet approval in the audit store
- `step:{approvalID}` → patches the step approval in the audit store

**Request body:**

```json
{
  "resolution":       "approved",
  "resolved_by":      "alice",
  "reason":           "Findings look correct, proceed with manual review",

  // Gate-specific (ignored for fleet/step):
  "approval_mode":    "review",
  "approval_session": ""
}
```

| Field | Description |
|---|---|
| `resolution` | `"approved"` or `"denied"` |
| `resolved_by` | Operator identity; defaults to `X-User` header if omitted |
| `reason` | Optional free-text reason; recorded in the audit trail |
| `approval_mode` | Gate only: approval mode for the triggered remediation playbook |
| `approval_session` | Gate only: session token when `approval_mode=session` |

**Examples:**

```bash
# Approve a gate
curl -X POST http://localhost:8080/api/v1/decisions/gate:plr_a3f7c1b2/resolve \
  -H "Content-Type: application/json" \
  -d '{"resolution": "approved", "resolved_by": "alice", "approval_mode": "review"}'

# Deny a fleet approval
curl -X POST http://localhost:8080/api/v1/decisions/fleet:apr_c8d2e1f4/resolve \
  -H "Content-Type: application/json" \
  -d '{"resolution": "denied", "resolved_by": "oncall", "reason": "Wrong maintenance window"}'
```

---

## Notifications

The Decision Hub fires a notification whenever a decision is created or resolved. Configure via environment variables:

| Variable | Description |
|---|---|
| `HELPDESK_DECISION_WEBHOOK` | Webhook URL for all decision events (generic JSON or Slack incoming webhook) |
| `HELPDESK_DECISION_WEBHOOK_SECRET` | HMAC-SHA256 key; adds `X-Helpdesk-Signature: sha256=<hex>` header |
| `HELPDESK_BASE_URL` | Gateway public URL; used to build absolute `resolve_url` links in notifications |
| `HELPDESK_SMTP_HOST` / `PORT` / `USER` / `PASSWORD` | SMTP server for email notifications |
| `HELPDESK_EMAIL_FROM` | Sender address |
| `HELPDESK_EMAIL_TO` | Comma-separated recipient list |

### Webhook payload

All events use the same payload shape:

```json
{
  "event":        "decision_pending",
  "decision_id":  "gate:plr_a3f7c1b2",
  "type":         "gate",
  "status":       "pending",
  "summary":      "Triage complete — TRANSITION_TO pbs_vacuum_remediate",
  "requested_by": "alice",
  "resolve_url":  "POST https://helpdesk.internal/api/v1/decisions/gate:plr_a3f7c1b2/resolve",
  "extra":        { "gate_type": "transition", "transition_target": "pbs_vacuum_remediate", "findings": "..." },
  "timestamp":    "2026-06-01T14:23:01Z"
}
```

`event` is `decision_pending` on creation and `decision_resolved` on approval/denial. For escalation gates, `extra.gate_type` is `"escalation"` and the target field is `escalation_target` instead of `transition_target`.

### Slack detection

If `HELPDESK_DECISION_WEBHOOK` contains `slack.com`, the payload is automatically wrapped in Slack's `attachments` format with colour coding:
- Yellow — `pending`
- Green — `approved`
- Red — `denied`, `abandoned`, `expired`

### HMAC signing

When `HELPDESK_DECISION_WEBHOOK_SECRET` is set, every outbound webhook request includes:

```
X-Helpdesk-Signature: sha256=<hex(HMAC-SHA256(body, secret))>
```

Verify on the receiving end with `hmac.Equal` to authenticate the payload.

All notification sends are non-blocking (goroutine-based); failures are logged at Warn level and never block the gateway.

---

## Relationship to type-specific endpoints

The Decision Hub is an additional surface, not a replacement. The existing endpoints remain valid:

| Type | Existing endpoint | Hub equivalent |
|---|---|---|
| Gate | `POST /api/v1/fleet/playbook-runs/{id}/proceed-escalation` | `POST /api/v1/decisions/gate:{id}/resolve` |
| Fleet approval | `PATCH {auditdURL}/v1/approvals/{id}` | `POST /api/v1/decisions/fleet:{id}/resolve` |
| Step approval | `PATCH {auditdURL}/v1/approvals/{id}` | `POST /api/v1/decisions/step:{id}/resolve` |

The hub routes to the same backend as the type-specific endpoint — they are interchangeable.

---

## Git webhook adapter (opt-in)

Operators can merge a specially-named branch to resolve a decision without calling the API directly. This works with any git provider that supports merge webhooks — GitHub, GitLab, Gitea, or a custom internal system.

### How it works

1. Operator creates a branch named `approved/gate/{runID}` (or `approved/fleet/{approvalID}`)
2. Operator merges the PR/MR into any target branch
3. The git provider sends a merge event to `POST /api/v1/webhooks/git`
4. The gateway extracts the branch name, maps it to a decision ID  and calls resolve

The gateway itself only needs to be reachable from the git provider — no git client is needed inside the gateway.

### Branch naming convention

| Branch | Maps to | Effect |
|---|---|---|
| `approved/gate/{runID}` | `gate:{runID}` | Approve a playbook gate |
| `approved/fleet/{approvalID}` | `fleet:{approvalID}` | Approve a fleet job |

The `approved/` prefix is configurable via `HELPDESK_GIT_RESOLVE_BRANCH` (default: `approved/`).

### Configuration

| Variable | Description |
|---|---|
| `HELPDESK_GIT_WEBHOOK_SECRET` | HMAC-SHA256 key for verifying `X-Hub-Signature-256` (GitHub/Gitea) or `X-Gitlab-Token` (GitLab). Leave empty to skip signature validation (not recommended for production). |
| `HELPDESK_GIT_RESOLVE_BRANCH` | Branch prefix that triggers resolution (default: `approved/`). |

Register the webhook endpoint in your git provider:

```
POST https://helpdesk.internal/api/v1/webhooks/git
```

The endpoint accepts all three webhook formats automatically:

| Provider | Payload format |
|---|---|
| GitHub / Gitea | `{"action":"closed","pull_request":{"merged":true,"base":{"ref":"approved/gate/..."}}}` |
| GitLab | `{"object_kind":"merge_request","object_attributes":{"state":"merged","target_branch":"approved/gate/..."}}` |
| Generic | `{"branch":"approved/gate/..."}` |

Non-merge events and non-matching branches return `200 OK` silently (no action taken).

---

## K8s and Docker — emit-and-wait

In containerised environments without a controlling terminal (`/dev/tty`), faulttest can poll the hub instead of reading from the TTY:

```bash
go run ./testing/cmd/faulttest run \
  --ids db-tx-lock-chain-blocker \
  --via-gateway --gateway http://helpdesk:8080 \
  --remediate --gate-escalation --emit-and-wait \
  --approval-mode manual \
  --audit-url http://auditd:7070
```

With `--emit-and-wait`:
- **Gate**: faulttest logs `Gate pending — resolve_url=...` and polls `GET /api/v1/fleet/playbook-runs/{id}` every 15 seconds until the gate is resolved externally.
- **Step**: faulttest long-polls `GET {auditURL}/v1/approvals/{id}/wait` and proceeds once the operator resolves the approval via the Decision Hub or the type-specific endpoint.

This makes faulttest safe to run inside a Kubernetes Job or a Docker container where `/dev/tty` is not available.

---

## Operational SRE/DBA Flywheel — fleet scenarios

See [here](VAULT.md#the-operational-sredba-flywheel) for details on aiHelpDesk Operational SRE/DBA Flywheel (and for more informal context, see [this blog post](https://medium.com/google-cloud/your-sre-on-call-runbook-is-already-obsolete-heres-why-that-s-not-your-fault-0a82b3b0183c#7fe7)). The Decision Hub is the coordination layer for all three fleet campaign scenarios:

| Scenario | Decision types involved |
|---|---|
| Fault catalog as threat model | `gate` — analyst reviews triage before each remediation playbook |
| Fleet GameDay | `gate` + `fleet_approval` — gate fires per-target; top-level approval gates the wave rollout |
| Incident-to-fleet campaign | `gate` + `fleet_approval` + `step_approval` — full review stack for production changes |

In all three cases, notifications fire automatically when a decision opens; the operator resolves via the hub, a direct API call, or a git branch merge.
