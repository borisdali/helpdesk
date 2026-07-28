# aiHelpDesk: Rights in the Flywheel

This document maps the ten entitlements stated in the [Customer Bill of Rights](CUSTOMER_RIGHTS.md) onto
the [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel). The flywheeel is the loop
that turns resolved incidents into better-calibrated playbooks. The canonical
description of the loop's mechanics (the consistency gate, resolution rate, accuracy
rate, etc.) is available [here](VAULT.md). *This* page exists for a narrower purpose: showing which Right fires at which stage
and drawing a hard line between what you can verify yourself in five minutes today and
what only compounds with real usage over time.

The distinction matters because of a specific objection: an approval gate, on its own,
reads as a liability disclosure: "the AI might be wrong, so we make you click OK." That's
honest, but it doesn't build confidence in the diagnosis. The Flywheel is what flips it.
The gate stops being "we're not sure, please confirm" and becomes a safety net around a
system that already has a track record. This is because by the time you see the gate, you can
also see how often this playbook has been right before. And that makes all the difference.

---

## The loop, rights-mapped

```
                    ┌──────────────────────────────┐
                    │   INCIDENT / ALERT FIRES     │
                    │   trigger_context captured   │
                    │        (Right IX)            │
                    └──────────────┬───────────────┘
                                   ▼
                    ┌──────────────────────────────┐
              ┌────▶│   AGENT DIAGNOSIS            │
              │     │   hypothesis + confidence    │
              │     └──────────────┬───────────────┘
              │                    ▼
              │     ┌──────────────────────────────┐
              │     │   CONSENT GATE               │
              │     │   action + reasoning +       │
              │     │   blast radius   (Right I)   │
              │     └──────┬─────────────────┬─────┘
              │        approved           denied
              │            ▼                 ▼
              │     ┌───────────┐    ┌─────────────────────┐
              │     │ EXECUTION │    │ DENIAL RECORDED     │
              │     └─────┬─────┘    │ not erased, not     │
              │           │          │ overridden (Right V)│
              │           ▼          └─────────┬───────────┘
              │     ┌─────────────────────────────────┐
              │     │   OUTCOME RECORDED              │◀────┘
              │     │   hash-chained audit (Right III)│
              │     └──────────────┬──────────────────┘
              │                    ▼
              │     ┌────────────────────────────────┐
              │     │   VAULT / CALIBRATION UPDATES  │
              │     │   track record (Right IV)      │
              │     │   judge-checked  (Right II)    │
              │     └──────────────┬─────────────────┘
              │                    │
              │         enough runs accumulate
              │                    ▼
              │     ┌──────────────────────────────────┐
              │     │  JUDGE REVIEWS PROPOSED          │
              │     │  PLAYBOOK IMPROVEMENTS (Right VI)│
              │     └──────────────┬───────────────────┘
              │                    ▼
              │     ┌──────────────────────────────────┐
              └─────┤  PLAYBOOK VERSION BUMPS          │
                    │  next incident: better-calibrated│
                    └──────────────────────────────────┘
```

Solid path = one full turn of the loop, provable in a single demo run. The bottom loop
(judge review → version bump) needs accumulated data over time; it is explained here, not
demoed live. See [here](JUDGMENT_LAYER.md) for the mechanism.

---

## Walking the loop

1. **Incident / alert fires — Right IX (The Original Claim).** The triggering context —
   the alert text, the threshold that fired — is stored on the run record, not just
   passed to the agent. You can always compare what monitoring reported to what
   aiHelpDesk diagnosed.
2. **Agent diagnosis.** The agent forms a root-cause hypothesis with a confidence level,
   using the active Playbook's guidance.
3. **Consent gate — Right I (Informed Consent).** Nothing executes until the action,
   the reasoning and the blast radius have been reviewed. This is the step that reads as
   a liability disclosure in isolation — which is exactly why it should never appear
   without the next two stages next to it.
4. **Approved → execution → outcome recorded — Right III (Full Audit Trail).** Every
   tool call, every approval, the full reasoning chain: hash-chained, tamper-evident.
5. **Denied → denial recorded — Right V (The Right to Refuse).** A denial is a
   first-class outcome, not an error. It is permanently recorded alongside the reason —
   not erased, not overridden after the fact.
6. **Vault / calibration updates — Right IV (The Grade) and Right II (Second Opinion).**
   The outcome feeds the playbook's track record. Diagnosis accuracy, when feedback is
   submitted, is checked independently rather than taken on the word of a single model
   run.
7. **Judge reviews proposed improvements → playbook version bumps — Right VI (The Right
   to Override).** Once enough runs accumulate, `vault suggest-update` proposes a
   revision; an independent LLM judge reviews it; a human decides. No draft activates
   without that decision.

---

## What you can verify today, in five minutes

This is not a slide. Every stage above is something you can [go watch happen](../deploy/docker-compose/DEMO.md) against a
real injected fault on your own machine:

```bash
export COMPOSE_FILE=docker-compose.demo.yaml
docker compose up -d
docker compose run --rm demo-runner
```

What that one run actually produces, unedited, from a real session running this demo:

- **Right IX**, verified by curl against the run record:
  `trigger_context: "PagerDuty P1: demo-postgres connections at 26/30 (87%) — pool near
  exhaustion, threshold monitor fired 2 min ago."`
- **Right IV**, printed in the section:
  `This run: #5 outcome: resolved` / `Track record: 4 prior run(s) resolution rate: 60%`
- **Right V**, exercised in `DEMO_MODE=clamping` (Mode C): a denied gate, outcome
  `abandoned`, permanently recorded — confirmed live, not asserted.

That 60% is real, not cherry-picked. It includes runs interrupted mid-session during
this project's own testing. That is the point: the number is whatever actually happened,
not whatever makes the best screenshot. Run it again and the track record moves, live, in
front of you. That motion *is* the flywheel turning.

---

## The second layer: the 3D Stability Cert

The track-record number above answers "has this playbook worked before?" It does not
answer the question a sophisticated buyer asks next: "how do I know that number itself is
reliable, that it isn't a fluke or noise from a single lucky run?"

That's what the [three-dimensional stability cert](ATTRIBUTION_CERTS.md) is there for:
_outcome_ (did it pass?), _conclusion_ (did the agent reach the same diagnosis every
run?), _evaluation_ (did the judge agree with itself?). It is deliberately not the
headline. It's the pre-empt: the answer to the follow-up question, one layer down, for
the reader who's already convinced by the loop and is now asking about the loop's own
instrumentation. See [here](CONSISTENCY.md) for the full mechanism and [this post](https://itnext.io/trust-has-three-dimensions-demand-from-your-ai-sre-vendor-to-show-a-cert-with-all-three-0f361a443807) for more color.

3D stability cert doesn't require a second demo. It's the same loop, run N times instead of once and it
shows up as a second, dimmer line in the same calibration section with the format of
`Fault cert: STABLE(N) @ model: <model>  pass: X% ±Ypp`, not a separate flow. To populate
it for the demo's own faults:

```bash
cd ~/cassiopeia/helpdesk
go run ./testing/cmd/faulttest run \
  --ids db-max-connections,db-long-running-query,db-tx-lock-chain-blocker \
  --repeat 5 \
  --external \
  --conn "host=localhost port=5434 dbname=postgres user=postgres password=demopassword sslmode=disable" \
  --agent-conn "host=demo-postgres port=5432 dbname=postgres user=postgres password=demopassword sslmode=disable" \
  --gateway http://localhost:8180 \
  --api-key demo-api-key \
  --agent-model claude-haiku-4-5-20251001
```

`--conn` is what `faulttest` uses locally to inject the fault (reachable from the host via
the Docker-published port); `--agent-conn` is what gets sent to the agent (the internal
Docker DNS name the agent's own infrastructure registry actually has on file). They have
to differ because `faulttest` runs outside the compose network and the agent runs inside
it — see the note further down if you're wondering why that split exists.

Real certs, generated for this demo's three faults on 2026-07-28:

| Fault | Verdict | Pass rate | Confidence range | Attribution |
|---|---|---|---|---|
| `db-max-connections` | STABLE(5) | 100% | ±4pp | `idle-connection-accumulation` (5/5, consistent) |
| `db-long-running-query` | STABLE(5) | 100% | ±4pp | `lock-contention-blocking-queries` (5/5, consistent) |
| `db-tx-lock-chain-blocker` | STABLE(5) | 100% | ±5pp | `active-long-running-statement-blocker` (4/5) + `cascaded-lock-chain` (1/5) |

`db-max-connections` is worth calling out specifically: the first attempt at generating
this cert (without `--agent-conn`) also came back `STABLE(5)`, but with attribution
`connection-pool-saturation (3/5), consistent: no (split), UNKNOWN: 2` — the agent was
intermittently failing a registry lookup unrelated to the fault itself and reporting that
failure as a finding. Same fault, same model, same 5 reps; fixing the connection-string
split alone took it from a 3/5 split to a clean 5/5. That's the stability cert doing
exactly its job: catching noise in the measurement, not just the outcome.

`--agent-model` must match the model the demo actually runs because the cert is deliberately model-scoped
(Right VIII), so a mismatched model produces a cert that never shows up in this demo's
section.

---

## See also

| Doc | What it covers |
|---|---|
| [VAULT.md](VAULT.md#the-operational-sredba-flywheel) | The canonical Flywheel loop and its CLI mechanics |
| [CUSTOMER_RIGHTS.md](CUSTOMER_RIGHTS.md) | The full ten-right commitment, with verification commands |
| [CONSISTENCY.md](CONSISTENCY.md) | The 3D stability cert: what it measures, how to run it |
| [ATTRIBUTION_CERTS.md](ATTRIBUTION_CERTS.md) | Outcome / conclusion / evaluation, in full |
| [JUDGMENT_LAYER.md](JUDGMENT_LAYER.md) | The judge review → version bump stage of the loop |
| [SECOND_OPINION.md](SECOND_OPINION.md) | The independent-check layers behind Right II |
| [DEMO.md](../deploy/docker-compose/DEMO.md) | How to run the demo referenced above |
