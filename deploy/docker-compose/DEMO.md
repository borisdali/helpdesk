# aiHelpDesk: 10-Minute Demo

See aiHelpDesk's governed AI incident response in action: a real [fault injected](../../docs/FAULTTEST.md)
into a real PostgreSQL instance, an AI agent that diagnoses it, a [step-approval gate](../../docs/PLAYBOOKS.md#informed-gate)
that surfaces before any destructive action and a tamper-proof [audit trail](../../docs/AUDIT.md)
of everything that happened.

No configuration required beyond an LLM API key.

---

## Prerequisites

- Docker Desktop (Mac/Windows) or Docker Engine + Compose plugin (Linux)
  — OR — Podman with `podman compose` (see [Podman note](#podman-users))
- An LLM API key (Anthropic **or** Google/Gemini — pick one)

---

## 1. Set your API key

**Anthropic:**
```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

**Google / Gemini:**
```bash
export GOOGLE_API_KEY=AIza...
# or
export GEMINI_API_KEY=AIza...
```

The demo auto-detects which key is set and picks the right model
(`claude-haiku-4-5-20251001` for Anthropic, `gemini-2.5-flash` for Google).
To override: `export HELPDESK_MODEL_VENDOR=anthropic HELPDESK_MODEL_NAME=claude-sonnet-4-6`.

---

## 2. Start the demo stack

```bash
docker compose --profile demo up -d
```

This starts four containers — a local Postgres (port 5434), the database agent,
the gateway (port 8180) and the audit daemon. First run pulls images (~150 MB).
Subsequent runs start in seconds.

Watch readiness:
```bash
docker compose --profile demo ps
```

All four services should reach `healthy` within ~30 seconds.

---

## 3. Run the demo

```bash
docker compose --profile demo run --rm demo-runner
```

The demo runner:
1. Injects the chosen fault into the local Postgres
2. Triggers the `pbs_connection_triage` playbook (or whichever matches the fault)
3. Streams the agent's diagnosis in real time
4. Pauses at the **step-approval gate** — showing the proposed action, action
   class and blast radius before anything executes
5. Waits for your approval (interactive mode) or auto-approves after a countdown
6. Executes the remediation and shows the post-recovery state
7. Prints the audit trail commands to explore

---

## Choose a fault

| Fault | What it demonstrates |
|---|---|
| `db-max-connections` (default) | Connection pool exhaustion — agent proposes `terminate_idle_connections`, gate shows blast radius |
| `db-long-running-query` | Slow query detection — agent proposes `cancel_query` |
| `db-tx-lock-chain-blocker` | Lock chain with root blocker — agent traces the chain and terminates the root |

```bash
# Connection pool exhaustion (default)
docker compose --profile demo run --rm demo-runner

# Slow query
DEMO_FAULT=db-long-running-query docker compose --profile demo run --rm demo-runner

# Lock chain
DEMO_FAULT=db-tx-lock-chain-blocker docker compose --profile demo run --rm demo-runner
```

---

## Choose an approval mode

**Mode B — Interactive (default)**

The demo pauses at the step-approval gate and waits for you to press Enter.
This is the most faithful representation of how aiHelpDesk works in production:
the agent diagnosed the fault, proposed a remediation step and now a human
decides whether to proceed.

```bash
docker compose --profile demo run --rm demo-runner
# or explicitly:
DEMO_MODE=interactive docker compose --profile demo run --rm demo-runner
```

**Mode A — Auto-approve**

The gate is shown with a countdown and approved automatically. Useful for
running the full flow unattended (CI demos, screen recordings).

```bash
DEMO_MODE=auto docker compose --profile demo run --rm demo-runner

# Adjust the countdown (default 8 seconds):
DEMO_MODE=auto DEMO_AUTO_APPROVE_SECS=5 docker compose --profile demo run --rm demo-runner
```

---

## Explore the audit trail

After the demo runs, the full audit trail is in the demo audit store:

```bash
# Recent audit events (hash-chained)
curl -s http://localhost:8180/api/v1/audit/events?limit=10 \
     -H 'Authorization: Bearer demo-api-key' | jq .

# The journey: WHAT the agent did + WHY (hypothesis, evidence, reasoning)
curl -s 'http://localhost:8180/api/v1/audit/journeys' \
     -H 'Authorization: Bearer demo-api-key' | jq .

# Playbook runs
curl -s http://localhost:8180/api/v1/fleet/playbook-runs \
     -H 'Authorization: Bearer demo-api-key' | jq .
```

---

## Run multiple faults back to back

```bash
for fault in db-max-connections db-long-running-query db-tx-lock-chain-blocker; do
  DEMO_FAULT=$fault DEMO_MODE=auto docker compose --profile demo run --rm demo-runner
done
```

---

## Tear down

```bash
docker compose --profile demo down -v
```

This removes all demo containers and volumes. Your production aiHelpDesk
deployment (if running) is unaffected — the demo profile uses separate
containers, ports and volumes.

---

## Podman users

`podman compose` is a drop-in replacement for `docker compose` for this demo.
The only required change: the sysadmin-agent in the production stack mounts
`/var/run/docker.sock`, which Podman exposes at a different path. The demo
profile does not use the sysadmin-agent, so no adjustment is needed.

```bash
# Podman (rootless, macOS/Linux)
podman compose --profile demo up -d
podman compose --profile demo run --rm demo-runner
```

If `podman compose` is not installed: `pip install podman-compose`.

---

## What you just saw

```
  alert fires                (fault injected — connection pool saturated)
       ↓
  agent diagnoses            (reads pg_stat_activity, identifies idle blocker)
       ↓
  step-approval gate         (blast radius: N connections; action class: destructive)
       ↓
  human approves             (you pressed Enter — or auto-approved after countdown)
       ↓
  agent executes             (terminate_idle_connections runs)
       ↓
  agent verifies recovery    (confirms connection count dropped below threshold)
       ↓
  audit trail sealed         (hash-chained, tamper-proof, queryable)
```

This is aiHelpDesk's L2 autonomy model: the agent handles diagnosis and proposes
the action; a human controls execution. Every step is recorded.

The audit trail that just ran is the same infrastructure that satisfies
[Right III (The Right to the Full Audit Trail)](../../docs/CUSTOMER_RIGHTS.md)
in the aiHelpDesk Customer Bill of Rights.

---

## Next steps

- **Connect to your own database:** set `DEMO_CONN` to your connection string
  and run the demo-runner against real infrastructure
- **Full deployment:** see [README.md](README.md) for production setup
- **Fault catalog:** [docs/FAULTTEST.md](../../docs/FAULTTEST.md) — 32 fault
  scenarios with injection specs and playbooks
- **Governance reference:** [docs/AIGOVERNANCE.md](../../docs/AIGOVERNANCE.md)
- **Who this is for:** [docs/FOR_WHOM.md](../../docs/FOR_WHOM.md)
