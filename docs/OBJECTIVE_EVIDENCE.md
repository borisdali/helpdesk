# aiHelpDesk Objective Evidence

A deterministic, code-derived fact-check layered on top of every diagnosis: does the
model's own claim actually match what a tool really returned? Not another model forming
another opinion — a plain comparison between a typed value read directly off a tool's
result and the value the model quoted back in its own required evidence line.

This is Layer 3 of aiHelpDesk's LLM fabrication detection — see
[AIGOVERNANCE.md §1.1](AIGOVERNANCE.md#11-llm-fabrication-detection) for how it relates to
Layer 1 (intra-agent post-mutation verification) and Layer 2 (inter-agent audit-based
delegation verification) and closes that section's own previously-open "read-only tool
output content — fabrication not detected" gap, for the specific tools and signals it
covers.

---

## Table of Contents

1. [The problem](#1-the-problem)
2. [Two halves: firing a signal, then confirming it](#2-two-halves-firing-a-signal-then-confirming-it)
3. [Firing a signal: probes and rules](#3-firing-a-signal-probes-and-rules)
4. [Confirming a signal: the confirmation registry](#4-confirming-a-signal-the-confirmation-registry)
5. [Gateway wiring: gate, warn or stay silent](#5-gateway-wiring-gate-warn-or-stay-silent)
6. [What's shipped today](#6-whats-shipped-today)
7. [Authoring a new rule](#7-authoring-a-new-rule)
8. [History: from gate-on-presence to gate-on-contradiction](#8-history-from-gate-on-presence-to-gate-on-contradiction)
9. [Known gaps](#9-known-gaps)
10. [See also](#10-see-also)

---

## 1. The problem

An agent's diagnosis is only as trustworthy as its self-reporting. `CONFIDENCE: 0.95` is
the model grading its own work. A `HYPOTHESIS_1 ... EVIDENCE: "..."` quote is the model
asserting it looked at real data, but nothing structurally stops a model from writing a
plausible-sounding quote that doesn't actually match what a tool returned, especially
under time pressure or with a smaller/cheaper model.

Two things a purely LLM-based check (a judge, a second model) can't give you here:

- **Determinism.** A judge is itself a probabilistic system reading probabilistic text —
  it can be fooled the same way the original model can, just less often.
- **A ground truth to check against.** A tool's structured result (a Pod's real restart
  count, a replication slot's real `active` flag) is a fact, not a claim. Objective
  evidence reads that fact directly, before it's ever summarized into prose for the LLM.

## 2. Two halves: firing a signal, then confirming it

The mechanism has two independent stages, both living in `internal/evidence`:

1. **A probe fires a signal.** A tool's typed result is thresholded against an
   operator-authored YAML rule (e.g. "did `oom_killed` come back `true`?"). This runs
   agent-side, at tool-call time, independent of anything the model says.
2. **A confirmation check decides whether the signal is already accounted for.** The
   Gateway compares the fired signal's real value against what the model's own response
   actually claimed (e.g. does its required verbatim `EVIDENCE` quote contain the real
   restart count?). This runs Gateway-side, after the model has responded.

A fired-but-unconfirmed signal is the interesting case: real evidence exists and the
model's own response never demonstrably engaged with it. That's what forces a human
gate. A fired-and-confirmed signal is not a problem — it's proof the model saw the real
data and is reported for visibility only.

## 3. Firing a signal: probes and rules

`internal/evidence`'s `ToolSchema[T]` lets a tool register a small, fixed set of named,
type-safe probes against its own typed result type `T`:

```go
var podSchema = evidence.NewToolSchema[Pod]("get_pods", func(p Pod) string { return p.Name }).
    Bool("oom_killed", func(p Pod) bool { return p.OOMKilled }).
    Numeric("restart_count", func(p Pod) float64 { return float64(p.RestartCount) }).
    Register()
```

An operator-editable YAML file then says which probes to threshold, at what value and
what signal results — referencing probes **by name**, never by field path:

```yaml
- tool: get_pods
  probe: oom_killed
  operator: "=="
  threshold: true
  signal: oom_killed
  detail: "Pod %s was OOMKilled (oom_killed=%v)"
```

This is deliberately not a generic reflection/JSONPath system. A rule naming an unknown
probe or an operator/threshold shape mismatched to the probe's kind (e.g. `>` against a
bool probe), is a load-time error (`LoadRules` fails, the agent doesn't start) — never a
rule that silently never fires. The one thing static validation can't catch is a
semantically wrong, but type-valid threshold (the right shape, the wrong number) — that's
a business decision, not a type error.

Rules for the same tool are evaluated in order; the first match per item wins. A tool can
have more than one active probe fire independently — a Pod that's both restarted and
triggered a `FailedScheduling` event produces two distinct signals, not one.

## 4. Confirming a signal: the confirmation registry

The model's response is a second, fixed-shape resource the same declarative framework
can probe — `internal/evidence/confirm.go`'s `HopOutcome` (the Gateway's parsed view of
a hop: its `DiagnosticReport`, raw response text and whether a signal line was present
at all) plays the same role a tool's typed result plays on the probe side.

Three confirmation probes ship today:

| Probe | Kind | What it checks |
|---|---|---|
| `evidence_quote_contains_value` (default) | bool | Does the primary hypothesis's required verbatim `EVIDENCE` quote contain a string form of the signal's real fired value? |
| `resource_named_in_quote` | bool | Does the response mention the same resource (Pod name, replication slot) the evidence is about, anywhere in its text? Used for bool-kind signals where quoting a bare `"true"` is unnatural. |
| `primary_confidence` | numeric | The primary hypothesis's own self-reported confidence — needs an explicit threshold, no universal default. |

A `Rule` can declare `confirmation_probe`/`confirmation_operator`/`confirmation_threshold`
to opt into a non-default probe; these are validated at `LoadRules` time exactly like the
signal-firing `probe`/`operator`/`threshold` fields are. The confirmation rule travels
with the fired event itself (agent → gateway, plain JSON), so the Gateway needs no file
access to an agent's own rules YAML to evaluate it.

An unrecognized confirmation probe name (e.g. a Gateway running an older binary than the
agent that posted the event) is treated as **unconfirmed** — fails toward the more
conservative, pre-existing gate behavior, never toward silently trusting a rule the
Gateway doesn't recognize.

## 5. Gateway wiring: gate, warn or stay silent

`cmd/gateway/playbooks.go`'s `objectiveEvidenceSignals(events, outcome)` partitions every
distinct signal recorded for a hop into `all` (reported unconditionally, for audit/CLEAN-
cert visibility) and `unconfirmed` (the subset that actually matters for gating).

What happens next depends on whether the hop chained onward:

- **A hop that emits `TRANSITION_TO`/`ESCALATE_TO`:** any unconfirmed signal forces a
  `pending_gate` response — `gate_reason: "objective_evidence:<signal>[,<signal>...]"` —
  regardless of the model's stated confidence or chosen next step. Confirmed evidence
  never gates.
- **A hop that resolves without chaining:** there's no next-hop candidate to gate
  approval for, so `recordSignalLessWarnings` instead surfaces a non-blocking
  `evidence_warnings` entry on the (still `resolved`) response.
- **Either way**, `objective_evidence_signals` (all fired signals) and the newer
  `objective_evidence_confirmed`/`objective_evidence_unconfirmed` breakdown are attached
  to the response, deduplicated and accumulated across every hop in the chain — so an
  operator can see confirmed-vs-unconfirmed without reverse-engineering it from the
  absence of a warning.

Low-confidence and objective-evidence checks are independent: a hop can trip either,
both or neither and both reasons are always reported together (`gate_reason` joins them
with `+`) rather than the first check found short-circuiting the second.

Full request/response shapes: [PLAYBOOKS.md § Objective-evidence gate](PLAYBOOKS.md#objective-evidence-gate).

## 6. What's shipped today

**K8s agent** (`agents/k8s/objective_evidence.yaml`, 6 rules):

| Tool | Signal | Confirmation |
|---|---|---|
| `get_pods` | `oom_killed` | `resource_named_in_quote` |
| `get_pods` | `pod_restarted` | default |
| `get_events` | `evicted` | default |
| `get_events` | `failed_scheduling` | default |
| `get_events` | `disk_pressure` | default |
| `get_events` | `memory_pressure` | default |

**Database agent** (`agents/database/objective_evidence.yaml`, 2 rules):

| Tool | Signal | Confirmation |
|---|---|---|
| `get_active_connections` | `idle_in_transaction_stuck` | default |
| `get_replication_status` | `replica_disconnected` | `resource_named_in_quote` |

`replica_disconnected` is a good example of a signal that isn't a property of any single
row — it's synthesized from three related queries (`get_replication_status` combines
role/recovery state, `pg_stat_replication` rows and `pg_replication_slots` rows into one
`ReplicationSummary` item: `Role == "Primary"`, zero connected replicas and at least one
inactive slot retaining WAL) before being probed like any other typed item. Deliberately
not "zero connected replicas alone," which would false-positive on a primary that
legitimately never had a replica attached.

**Not yet instrumented:** the sysadmin agent (`check_host`/`get_host_logs` — container
exit codes and log content are exactly the kind of typed, verifiable data this mechanism
is built for) and every other database/K8s tool beyond the eight rules above.

## 7. Authoring a new rule

Two layers of change, most of the time only the second is needed:

1. **New field to threshold, never exposed before:** register a new probe in the tool's
   own `tools.go` (`ToolSchema[T].Numeric/Bool/String`). A small, one-time Go change.
2. **Tune a threshold, rename a signal, enable/disable a rule, change confirmation
   behavior:** pure YAML — edit the agent's `objective_evidence.yaml` and restart it. No
   Go change, no rebuild.

Rules load via an env var, non-fatal on failure (logged at Error, agent still starts —
same convention as `HELPDESK_INFRA_CONFIG`, one severity level up given the governance
stakes):

```bash
HELPDESK_K8S_EVIDENCE_RULES=/etc/helpdesk/k8s-objective-evidence.yaml
HELPDESK_DB_EVIDENCE_RULES=/etc/helpdesk/db-objective-evidence.yaml
```

Unset means no rules load — the agent runs fine, just without this backstop.

## 8. History: from gate-on-presence to gate-on-contradiction

Through v0.25.0, the force-gate fired on **presence alone** — any hop with real objective
evidence got gated, regardless of what the model concluded. Fine while this only covered
K8s and never combined with a hop that also tried to chain onward.

v0.27.0's DB-agent rollout, combined with a real cross-domain escalation
(`db-replica-disconnected`'s DB→sysadmin chain), was the first time a hop both carried
real evidence *and* needed to chain to a correct next step — and a textbook-correct
diagnosis got gated identically to a wrong one, purely because the mechanism couldn't
tell the difference.

A keyword-matching design (mirroring `expected_keywords`) was considered and rejected:
fine for offline test scoring with a `--judge` escape hatch, too weak for a live
production gate — a model writing "I checked whether it had disconnected and it hadn't"
would keyword-match as confirmed while being completely wrong.

The shipped fix is the confirmation registry described in [§4](#4-confirming-a-signal-the-confirmation-registry):
a symmetric extension of the same declarative probes+YAML framework, treating the model's
own parsed response as a second typed resource rather than free text to pattern-match.
Presence of objective evidence the model correctly engaged with is now corroboration, not
a red flag — only a genuine, checkable contradiction still forces a gate.

## 9. Known gaps

- **Production deployment wiring is incomplete.** `HELPDESK_K8S_EVIDENCE_RULES` and
  `HELPDESK_DB_EVIDENCE_RULES` are plain env-var paths today, mirroring
  `HELPDESK_INFRA_CONFIG`'s pattern exactly, but neither the Dockerfile nor the Helm
  chart / docker-compose deployment actually mounts an `.example.yaml` variant the way
  `policies.yaml`/`users.yaml` already do. Until that's wired, a real deployment has to
  set this up manually to get the backstop at all.
- **Narrow by design, not yet by ceiling.** Eight rules across two agents is deliberately
  conservative — every signal here is a tool whose result already carries unambiguous
  structured evidence (a Pod's real restart count, a replication slot's real state).
  Extending coverage is a case-by-case decision per tool, not a mechanical rollout — see
  the design note in `cmd/gateway/playbooks.go`'s `objectiveEvidenceSignals`.
- **A broader, unscoped version of this idea is still backlogged.** Cross-checking any
  `EVIDENCE:` quote against the real `tool_execution` audit output — regardless of
  whether a declarative rule exists for that specific tool/signal — would generalize this
  mechanism's confirmation half beyond the eight rules above. Not started; distinct from
  the action-provenance delegation verification in
  [AIGOVERNANCE.md §1.1 Layer 2](AIGOVERNANCE.md#11-llm-fabrication-detection), which
  this would complement rather than replace.

## 10. See also

| Document | What it covers |
|----------|----------------|
| [AIGOVERNANCE.md §1.1](AIGOVERNANCE.md#11-llm-fabrication-detection) | The three fabrication-detection layers and where this one fits |
| [PLAYBOOKS.md § Objective-evidence gate](PLAYBOOKS.md#objective-evidence-gate) | Full request/response JSON shapes at the API level |
| [SECOND_OPINION.md](SECOND_OPINION.md) | The broader independent-verification philosophy this mechanism serves |
| [ATTRIBUTION_CERTS.md §9](ATTRIBUTION_CERTS.md#9-the-clean-axis) | How `objective_evidence_signals` feeds the CLEAN axis on a stability cert |
| [HA_DR.md §2](HA_DR.md#2-replica-disconnection-the-absent-row-edge-case) | The `db-replica-disconnected` case study this mechanism's confirmation-registry redesign came from |
