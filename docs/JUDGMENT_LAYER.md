# The Judgment Layer

aiHelpDesk's [design principles](PRINCIPLES.md#10-probabilism-is-bounded-measured-and-governed)
describe three architectural layers:

```
  Reasoning layer   → probabilistic, contextual, adaptive   (LLM)
  Governance layer  → deterministic, enforced, auditable     (policy engine, blast-radius)
  Execution layer   → deterministic, exact, logged           (tool calls)
```

There is a fourth layer and it belongs to the human operator.

Not because of compliance requirements (we have [Informed Consent](INFORMED_CONSENT.md) and [Compliance Reporting](COMPLIANCE.md) for that). 

Not because AI is untrustworthy in general (we combat it with the [Triage Consistency Certificate](CONSISTENCY.md) badge). 

But because there is a specific class of decisions in the
aiHelpDesk [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel) that require knowledge which cannot be derived from
incident traces and which the system is not designed to acquire on its own.

This page defines what that class of decisions is, why it matters and what
the workflow looks like when the human judgment layer has to override the AI's
own improvement proposals.

---

## Table of Contents

1. [What the Judgment Layer Is](#1-what-the-judgment-layer-is)
2. [Three categories of irreplaceable judgment](#2-three-categories-of-irreplaceable-judgment)
3. [The canonical case](#3-the-canonical-case)
4. [When vault suggest-update is wrong: the manual improvement path](#4-when-vault-suggest-update-is-wrong-the-manual-improvement-path)
5. [The SRE role question answered directly](#5-the-sre-role-question-answered-directly)
6. [See also](#6-see-also)

---

## 1. What the Judgment Layer Is

aiHelpDesk [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel) introduced a loop:
[Incidents](INCIDENTS.md) produce data, data produces drafts, drafts get activated, activated
[Playbooks](PLAYBOOKS.md) produce better incidents. At the center of that loop is the
[`vault suggest-update`](VAULT.md#vault-suggest-update) command where an LLM that reads failure traces and proposes
revised playbook guidance.

The judgment layer is what happens when that proposal is wrong.

Not wrong in a detectable way — protocol violations, missing FINDINGS lines,
structural regressions. Those are caught by [protocol validation](VAULT.md#protocol-validation)
and the [LLM-as-judge](LLM_AS_JUDGE.md). Wrong in a *semantic* way: the proposal
is coherent, well-reasoned, passes all automated checks and... would make the
failure rate worse.

When that happens, no automated system catches it. A human has to read the
proposal, understand why it fails on its own terms and write the correct
intervention instead. That is the judgment layer: not oversight of what the AI
did, but authorship of what the AI cannot write.

---

## 2. Three Categories of Irreplaceable Judgment

These are the failure modes that route to the judgment layer. They look like
AI errors. They are actually knowledge gaps. Knowledge that doesn't exist in
the audit trail and cannot be synthesised from it.

### 2.1 Operational Semantics

A finding can be correct as a fact and wrong as a recommendation. The AI
cannot distinguish between these two sentences:

- `idle_session_timeout is set to 0` — this is a root cause explanation
- `configure idle_session_timeout` — this is a long-term preventive action

Both appear in the same incident context. The trace shows the agent found the
setting, identified its role in the problem and reported it. The trace does
not contain the information that one of these is a diagnosis and the other
is a remediation category and that mixing them up produces the wrong
recommendation.

That distinction lives in the operator's operational experience. It is not
learnable from trace synthesis alone because the failure mode it prevents
is: *agent recommends the right long-term fix but omits the immediate action
that is the entire point of a triage playbook.*

### 2.2 Meta-Diagnosis

When `vault suggest-update` identifies a failure pattern and proposes a fix,
it performs two steps:

1. Diagnose why the agent failed
2. Propose a guidance change that addresses the diagnosis

Step 1 can be correct while step 2 is wrong. Specifically, the AI can correctly
identify that the agent made the wrong recommendation while incorrectly
concluding that the guidance needed to be *softer* (more validating of both
options) when the correct fix was to make it *harder* (explicitly prohibiting
the wrong option).

These require opposite interventions. From the outside — "agent recommended the
wrong thing" — they are indistinguishable. The AI defaults to "guidance was
ambiguous." The correct diagnosis is often "guidance permitted what it should
have prohibited."

A human who has seen the specific wrong recommendation before, in a real
production incident, recognises the second pattern immediately. The trace
doesn't contain that recognition.

### 2.3 Prohibition vs. Elaboration

The most actionable guidance is often a prohibition, not an instruction.
"Do NOT recommend X as your primary fix" is frequently more effective than
"Consider Y and Z before deciding." The AI tends toward elaboration because
elaboration is how it communicates reasoning. Prohibition requires
meta-knowledge: knowing not just what the right answer is, but what the
specific wrong answer is and why it's wrong.

That meta-knowledge — naming the trap, not just pointing at the target — is
the distillation of operational experience with a specific failure class. It
belongs in the playbook as a CRITICAL paragraph. It cannot be synthesised
from a trace where the agent committed the error; it can only be written by
someone who understands why the error is tempting and wants to foreclose it.

---

## 3. The Canonical Case

See [here](BENCHMARKING_SAMPLE9.md) for the actual commands and their output used to generate section.

**Fault:** `db-max-connections`. PostgreSQL connection pool at 95%. Cause:
`idle_session_timeout=0`, 200+ idle connections accumulating. Correct immediate
action: `kill_idle_connections`. Correct long-term action: configure
`idle_session_timeout`. Common misdiagnosis: recommending the long-term action
as the primary fix.

**v1.3 baseline (20 runs):**

```
VERSION     RUNS    SUCCESS%   AVG DIAG   AVG REMED   APPROACH OK
1.3         20      75%        89%        100%        100%
```

The failure pattern: 25% of runs ended with the agent correctly identifying
`idle_session_timeout=0` as the root cause, then recommending configuring it
as the primary fix. Remediation didn't run because the recommendation was wrong.
Not wrong factually. Categorically wrong — the right action for a different
time horizon.

**`vault suggest-update` output:** The AI identified the failure pattern correctly
and proposed adding a paragraph explaining that `idle_session_timeout=0` is a
common finding and that operators should consider configuring it.

**Judge verdict: `NEEDS_REVIEW`.**

The proposal passed all structural checks. It was readable and coherent. It
made the failure mode worse: it would have given the agent more confidence in
the wrong recommendation.

**Manual edit (v1.4):** Three targeted additions to the guidance field:

1. An explicit mandate: *"your immediate remediation recommendation MUST be
   `kill_idle`"* — closes the ambiguity entirely.

2. A CRITICAL paragraph naming the trap: *"finding that `idle_session_timeout`
   is disabled explains WHY idle connections accumulated. It is the root cause
   explanation, not the immediate action. Do NOT recommend configuring
   `idle_session_timeout` as your primary fix."*

3. A four-row decision table mapping symptom combinations to the correct
   recommended action.

**v1.4 result (1 run):**

```
VERSION     RUNS    SUCCESS%   AVG DIAG   AVG REMED   APPROACH OK
1.3         20      75%        89%        100%        100%
1.4 *        1      100%       100%       100%        100%
```

The structural fix worked because the human understood the specific branch
point the agent was failing at. The AI understood the symptom. The human
understood why the symptom had two superficially similar but opposite
causes — and wrote a prohibition that forecloses the wrong one.

For the full narrative, see the companion blog post linked in
[See also](#6-see-also).

---

## 4. When vault suggest-update Is Wrong: the Manual Improvement Path

`vault diff --judge` will produce `NEEDS_REVIEW` or `REJECT` when the proposal
is a regression. At that point the automated path ends. What follows is the
manual path.

### Step 1: Read the judge's reasoning, not just the verdict

```bash
faulttest vault diff <draft-id> \
  --judge \
  --judge-vendor anthropic \
  --judge-model claude-haiku-4-5-20251001 \
  --gateway $GW --api-key $KEY
```

The `Reasoning` line is the judge's one-sentence diagnosis of what the proposal
got wrong. Read it against the categories in §2 above. Is the proposal treating
an explanation as an action? Is it elaborating when it should be prohibiting?
Is it softening guidance that should be hardened?

If the judge says `NEEDS_REVIEW` but you can't articulate *why* the proposal
is wrong, don't discard it yet — run the fault again with the draft activated
in a staging environment and see whether the failure mode recurs. The verdict
is a gate, not a final answer.

### Step 2: Discard the AI draft

```bash
faulttest vault discard <draft-id> \
  --gateway $GW --api-key $KEY
```

The draft is gone from the review queue. You are now authoring v1.4 from scratch.

### Step 3: Find and edit the source playbook YAML

All system and maintained playbooks live in `playbooks/` at the repository root.
Custom playbooks may live elsewhere — check `vault active` for the series ID and
search for a matching YAML file.

```bash
# Find the source file
ls playbooks/
# e.g. playbooks/connection-triage.yaml

# Edit it
$EDITOR playbooks/connection-triage.yaml
```

**What to change:**

- Bump `version:` (e.g. `"1.3"` → `"1.4"`)
- Target the specific branch point where the agent fails. Do not rewrite the
  entire guidance field — that discards accumulated context that works correctly.
- Name the trap explicitly. If the failure mode is "agent recommends X when it
  should recommend Y," write: *"Do NOT recommend X as your primary action when
  Y is available."* A prohibition is harder to override than an instruction.
- Add a decision table if the guidance currently requires the agent to reason
  from principles at a branch point. Explicit lookup beats implicit reasoning at
  the edges.
- If the fault produces a new failure symptom pattern, add it to `symptoms:`.
- If the structured FINDINGS format needs a new field to capture the relevant
  signal, add it (and update the catalog's `narrative` field to match).

### Step 4: Import into the running gateway

Edited source files are not auto-seeded into a running deployment. System
playbooks are seeded at auditd startup; for a live update without restart, use
the import API.

```bash
# Read the file and POST to the import endpoint (parses, validates, returns draft)
YAML=$(cat playbooks/connection-triage.yaml)

DRAFT=$(curl -s -X POST "$GW/api/v1/fleet/playbooks/import" \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"text\": $(echo "$YAML" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'), \
       \"format\": \"yaml\", \
       \"hints\": {\"series_id\": \"pbs_connection_triage\"}}")

echo "$DRAFT" | jq '{warnings, series_id: .draft.series_id, version: .draft.version}'
```

The `/import` endpoint **parses and validates but does not persist**. Check
`warnings` — any protocol violation should be fixed in the source YAML before
proceeding. Then save the draft:

```bash
# Save the draft (persist it as an inactive version in its series)
PLAYBOOK_ID=$(curl -s -X POST "$GW/api/v1/fleet/playbooks" \
  -H "Authorization: Bearer $HELPDESK_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d "$(echo "$DRAFT" | jq '.draft')" \
  | jq -r '.id')

echo "Saved draft: $PLAYBOOK_ID"
```

### Step 5: Diff before activating

Even for a manual edit, run `vault diff` to confirm the change set is what you
intended and that no unintended fields changed:

```bash
faulttest vault diff $PLAYBOOK_ID \
  --gateway $GW --api-key $KEY
```

If you want the judge's assessment of your own edit:

```bash
faulttest vault diff $PLAYBOOK_ID \
  --judge --judge-vendor anthropic \
  --judge-model claude-haiku-4-5-20251001 \
  --gateway $GW --api-key $KEY
```

An `APPROVE` verdict here does not mean the edit is correct — the judge cannot
verify operational semantics any more than `suggest-update` could. What it
*does* catch is if your edit introduced a protocol violation or a structural
regression that you didn't notice.

### Step 6: Activate

```bash
faulttest vault activate $PLAYBOOK_ID \
  --gateway $GW --api-key $KEY

faulttest vault active --gateway $GW --api-key $KEY
```

The new version is live. No restart required. No image rebuild.

### Step 7: Re-run faulttest and measure

```bash
faulttest run --external \
  --ids db-max-connections \
  --remediate \
  --judge --judge-vendor anthropic --judge-model claude-haiku-4-5-20251001 \
  --gateway $GW --api-key $KEY \
  [... connection flags ...]
```

A single run at 100% is evidence the structural fix works; it is not
calibration. Run at least five times before drawing conclusions about whether
the failure mode is closed. A failure mode that recurs at a lower rate is
partially addressed — go back to step 3 and tighten further.

### Step 8: Confirm with vault diff (post-activation)

Once the new version has accumulated runs, use two-ID mode to produce a
permanent record of what changed between versions and what the data shows:

```bash
# Get both IDs from vault versions
faulttest vault versions pbs_connection_triage \
  --gateway $GW --api-key $KEY

# Post-activation diff — works even after both versions are already active
faulttest vault diff <v1.3-id> <v1.4-id> \
  --gateway $GW --api-key $KEY
```

This diff is auditable provenance: which guidance changed, between which
versions and traceable back to which incident run surfaced the gap. If a
future regression appears in v1.5, `vault diff v1.4 v1.5` will show exactly
what changed and `vault diff v1.3 v1.4` will show the fix that v1.5 may have
inadvertently undone.

---

## 5. The SRE Role Question Answered Directly

The case above is sometimes presented as evidence that AI cannot replace SREs.
That framing is too broad to be useful.

The more precise answer is this: the AI in the [flywheel](VAULT.md#the-operational-sredba-flywheel) handles **pattern
recognition at scale**. Across 20 runs of the same fault, it identifies the
consistent failure mode faster and more reliably than a human reading 20
incident reports. It synthesises a proposed fix. It does all of this without
anyone having to remember which runs were affected or write a script to extract
the signal.

What it cannot do — yet and possibly never — is distinguish between
two interventions that look identical from the pattern-recognition level, but
require opposite implementations. That failure mode requires:

- Knowing that `idle_session_timeout` configuration and `kill_idle` are in
  different categories (explanation vs. action, long-term vs. immediate).
- Knowing that the agent's failure was not "uncertainty about what to do", but
  "certainty about the wrong thing to do".
- Knowing that the fix for certainty-about-the-wrong-thing is a prohibition,
  not an elaboration.

None of this knowledge exists in the trace. It exists in the operational
experience of someone who has been on-call when the wrong recommendation was
followed. Or who wrote the playbook in the first place and knows what failure
mode it was designed to prevent.

**The maturing SRE role in the AI age is not "oversight of AI outputs."**
Oversight implies checking whether the system did what it was told. The judgment
layer is different: it is the source of the instructions that the system cannot
write for itself. Every CRITICAL paragraph in a playbook, every explicit
prohibition, every decision table that eliminates a branch point — those were
written by someone who understood the failure mode at a level the AI cannot
(yet?) reach from traces alone.

That knowledge compounds. The more precisely the guidance encodes what not to
do (and why), the narrower the space of wrong recommendations becomes and the
less often the judgment layer has to intervene. The [flywheel](VAULT.md#the-operational-sredba-flywheel) is working when
the judgment layer activates infrequently but decisively, when it does.

An SRE team that treats this as a role to be automated away will have fewer
interventions available when the AI's own improvement proposals regress. A team
that treats it as a specific, valuable cognitive contribution (knowing the
traps by name, encoding prohibitions precisely, measuring whether the
prohibitions hold) will build a system that improves faster than one that
relies on traces alone.

The question is not "will AI replace SREs?" The question is "which part of
SRE work is irreplaceable and are we investing in it deliberately?" This page
is the answer to the second question, for aiHelpDesk's [flywheel](VAULT.md#the-operational-sredba-flywheel) specifically.

---

## 6. See also

| Document | What it covers |
|----------|----------------|
| [PRINCIPLES.md](PRINCIPLES.md) | Principle #8 (Informed Consent), Principle #10 (Probabilism), Principle #11 (Model-neutral) |
| [VAULT.md](VAULT.md) | Full vault command reference; the flywheel loop; `vault suggest-update`, `vault diff`, `vault activate` |
| [LLM_AS_JUDGE.md](LLM_AS_JUDGE.md) | How `vault diff --judge` works; the `APPROVE`/`NEEDS_REVIEW`/`REJECT` verdict schema |
| [PLAYBOOK_OPS.md](PLAYBOOK_OPS.md) | Operational best practices for running playbooks during incidents |
| [CONSISTENCY.md](CONSISTENCY.md) | Triage Consistency Certification; measuring whether playbook changes hold under repeated runs |
| [PLAYBOOKS.md](PLAYBOOKS.md) | Playbook schema; the `guidance` field; `symptoms`, `escalation`, FINDINGS format |
