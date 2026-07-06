# aiHelpDesk Customer Bill of Rights

This is not a shared responsibility model. A shared responsibility model allocates
liability between vendor and customer. This document makes affirmative commitments —
things you are entitled to demand from aiHelpDesk, unconditionally, as a customer.

The distinction matters. "We provide audit logs; you are responsible for reviewing them"
is a liability pass-off. "You are entitled to a complete, tamper-proof, human-readable
audit trail of every action the system took, at any time, for any run, without
configuration" is a commitment.

Ten rights. For each: the entitlement, the mechanism that makes it real and — where the
mechanism has a current gap — an honest statement of what is pending and when it ships.

---

## I. The Right to Informed Consent

**You are entitled to see the complete diagnosis and proposed remediation plan before
any write or destructive action executes. Every time, without exception.**

The triage gate is mandatory. It is not configurable away. Before remediation runs, you
see: the root-cause hypothesis with supporting evidence, the confidence level, the proposed
steps, the blast-radius estimate and the rollback risk for any session with uncommitted
writes. Nothing executes until you have reviewed and approved this picture.

*Mechanism:* The [triage gate](INFORMED_CONSENT.md), `approval_mode` controls, Decision Hub.
*Status:* Solid. No gaps.

---

## II. The Right to a Second Opinion

**You are entitled to an independent check on every consequential AI claim: diagnosis,
remediation plan, playbook improvement proposal and confidence score.**

No claim that drives an action is taken on the word of a single model run. Diagnoses are
checked by operators at the gate. Playbook improvement proposals are checked by an
independent [LLM judge](LLM_AS_JUDGE.md) before they reach you. Confidence scores are checked by calibration
bands against historical accuracy. Stability is checked by repeated runs, not single
results.

*Mechanism:* [Second Opition](SECOND_OPINION.md): five layers, each documented with its
verification mechanism.
*Status:* Mostly solid. Three gaps being closed in v0.20.0: model-specific calibration
(Right IV), alert capture for diagnosis comparison (Right IX) and judge track record
measurement.

---

## III. The Right to the Full Audit Trail

**You are entitled to a complete, tamper-proof record of what the system did (every tool
call, every approval, every denial) and why it did it (the reasoning chain, the hypothesis
it formed, the evidence it cited, the confidence it reported). For every run, forever.**

WHAT and WHY are stored separately and cross-linked. From an incident, you can navigate
to the reasoning chain. From a reasoning chain, you can navigate to the incident outcome.
The audit log is hash-chained: any deletion or modification of a past record is detectable.

*Mechanism:* [Audit](AUDIT.md), [Journeys](JOURNEYS.md), tamper-evident hash chain
in `audit_events`.
*Status:* Solid. No gaps.

---

## IV. The Right to Know the Grade

**You are entitled to a queryable, model-specific, human-verified accuracy and calibration
figure for every diagnosis playbook in your fleet. Not a marketing claim, a number with
a data provenance chain.**

`vault calibration` shows whether the system's stated confidence predicts actual accuracy.
`vault accuracy` shows the per-playbook correctness rate. `vault versions` shows whether
accuracy is improving across playbook versions. All three are queryable at any time against
the live audit store.

*Mechanism:* `vault calibration`, `vault accuracy`, `vault versions` in [Vault](VAULT.md).
*Gap (v0.20.0):* Calibration data is currently blended across model versions (model cert PK
not yet in schema — see [Consistency](CONSISTENCY.md)). Calibration also currently reflects
LLM self-consistency when human feedback is sparse; the output warns you when this is the
case and `vault feedback` will provide a first-class path to submit human verdicts.
Both are being fixed in v0.20.0. Until then: check the `Sources:` line and `diagnosis_model`
field in `vault accuracy` output.

---

## V. The Right to Refuse

**You are entitled to deny any gate — at any step, for any reason — and have that denial
permanently recorded alongside your stated reason. No denial can be erased or overridden
after the fact.**

When you deny a gate, remediation does not execute. The denial reason is stored in the
tamper-proof audit log alongside the run ID, operator identity and timestamp. In
`agent_approve` mode, you can deny individual steps: approving step 1 (read) does not
commit you to step 2 (write). Denial is a first-class outcome, not an error.

*Mechanism:* Gate denial in [Informed Consent](INFORMED_CONSENT.md#the-three-parts),
`verdict_notes` in audit trail.
*Status:* Solid. No gaps.

---

## VI. The Right to Override

**When the AI's own improvement proposals are wrong, your judgment prevails. No draft
activates without a human decision and no automated system can override that decision.**

`vault suggest-update` proposes. The LLM judge reviews. You decide. A `NEEDS_REVIEW`
verdict routes the decision to you. Not to the next automated step. The system is designed
so that the most sophisticated error mode (AI proposes a confident, coherent change that
would worsen performance) is caught and escalated rather than shipped.

*Mechanism:* [Judgement Layer](JUDGMENT_LAYER.md) — the manual improvement path, the
three categories of irreplaceable human judgment, the workflow when `suggest-update` fails.
*Status:* Solid. No gaps.

---

## VII. The Right to Own Your Knowledge

**Your operational knowledge — every playbook, every guidance paragraph, every decision
table, every prohibition you wrote — is stored in open, human-readable YAML. It is not
encoded in model weights, not locked in a proprietary format, not held on aiHelpDesk
servers. You can inspect, edit, export, fork or import it at any time.**

System playbooks are versioned and readable in the `playbooks/` directory of the open
source repository. Custom playbooks are stored in the audit database and exportable via
`GET /api/v1/fleet/playbooks/{id}`. The format is documented and stable.

*Mechanism:* [Playbooks](PLAYBOOKS.md), `playbooks/` directory, open schema.
*Status:* Solid. No gaps.

---

## VIII. The Right to Switch Models

**A model upgrade does not silently invalidate your stability certificates or accuracy
figures. Upgrading from one model to another requires explicit re-certification under the
new model. The old cert remains in the record; it is not overwritten.**

Model-neutral design means your playbooks, governance rules, audit trail and tool
implementations are unaffected by model changes. The certification is model-specific and
explicit: `STABLE(5) [claude-sonnet-4-6]` is a different cert from `STABLE(5)
[claude-opus-4-8]`. You choose when to re-certify; the system does not do it for you
silently.

*Mechanism:* [Consistency](CONSISTENCY.md), [Principles](PRINCIPLES.md#11-model-neutral-by-design).
*Gap (v0.20.0):* Model identifier is stored as an annotation but is not yet part of the
cert primary key. Running with a new model currently overwrites the old cert. Fix ships
in v0.20.0 item #1. Until then: `diagnosis_model` field in `vault accuracy` shows which
model the current cert was issued against.

---

## IX. The Right to the Original Claim

**Every diagnosis is traceable to the triggering alert or context that initiated the run.
You can always compare what the monitoring system reported to what aiHelpDesk diagnosed.**

The triggering context — the PagerDuty alert text, the connection error message, the log
line that prompted the investigation — is stored alongside the run record and surfaced in
`vault incidents <run-id>`. The second opinion is only meaningful when the first opinion
is visible.

*Mechanism:* `trigger_context` field on `PlaybookRun`, surfaced in `vault incidents`.
*Gap (v0.20.0):* `trigger_context` is not yet persisted — it is passed to the agent but
not stored. Fix ships in v0.20.0 item #2. Until then: the `context` field in the run
request is available in the agent transcript (accessible via `vault journey`).

---

## X. The Right to Take Your Data

**aiHelpDesk is open source. Your audit trail, your playbooks and your operational
knowledge are yours. If you stop using aiHelpDesk, you take them with you.**

The audit database schema is documented and open. The playbook format is documented YAML.
The tool implementations are in the open source repository. There is no proprietary
lock-in format, no vendor-held operational knowledge, no feature that requires the managed
service to remain accessible.

*Mechanism:* [Principles](PRINCIPLES.md#1-aiHelpDesk-is-oss), open source repository,
documented schemas.
*Status:* Solid. No gaps.

---

## How to Verify Each Right

You should not take these commitments on faith. Here is how to test each one against a
running deployment:

```bash
# Right I — the gate exists and is mandatory
curl -X POST $GW/api/v1/fleet/playbooks/$PB_ID/run \
  -H "Authorization: Bearer $KEY" \
  -d '{"connection_string": "prod-db-1", "approval_mode": "manual"}' \
  | jq '{run_id, status}'
# → status should be "pending_approval", not "completed"

# Right III — the audit trail exists and is hash-chained
curl $GW/api/v1/audit/events?limit=5 -H "Authorization: Bearer $KEY" \
  | jq '.[].hash'
# → every event has a non-empty hash

# Right IV — calibration is queryable
faulttest vault calibration --gateway $GW --api-key $KEY
# → table shows bands; check Sources: line for human vs. auto_judge ratio

# Right VII — playbooks are exportable YAML
curl $GW/api/v1/fleet/playbooks/$PB_ID -H "Authorization: Bearer $KEY" \
  | jq -r '.guidance'
# → plain text, no proprietary encoding

# Right VIII — cert shows diagnosis_model
faulttest vault accuracy db-max-connections --gateway $GW --api-key $KEY
# → "Diagnosis model: claude-sonnet-4-6" appears in output

# Right IX — trigger_context on runs (v0.20.0)
curl $GW/api/v1/fleet/playbook-runs/$RUN_ID -H "Authorization: Bearer $KEY" \
  | jq '.trigger_context'
# → alert text that initiated the run
```

---

## The Commitment

Rights I, III, V, VI, VII and X are solid today. Rights II, IV, VIII and IX have named
gaps with named fixes in v0.20.0.

Publishing the gaps is the commitment. A vendor who claims all ten rights without
qualification is making claims they cannot verify. At aiHelpDesk we prefer to tell you exactly which
rights are fully implemented, which have known gaps and when those gaps close.

That honesty is itself a right. You are entitled to a vendor who tells you where the
system is not yet complete.

---

*See [SECOND_OPINION.md](SECOND_OPINION.md) for the technical detail behind Right II.
See [INFORMED_CONSENT.md](INFORMED_CONSENT.md) for the technical detail behind Rights I and V.
See [JUDGMENT_LAYER.md](JUDGMENT_LAYER.md) for the technical detail behind Right VI.*
