# aiHelpDesk: From Demo to Production

The [10-minute demo](../deploy/docker-compose/DEMO.md) proves the mechanism of the [fault injection testing](FAULTTEST.md) system and the whole [flywheel](VAULT.md#the-operational-sredba-flywheel) against a throwaway database. 
This page is the sequence for going from "I ran the
demo, I'm convinced" to "I'm safely pointed at my own infrastructure". In order, with
no config to assemble cold.

Three other docs already answer pieces of this. This page doesn't replace them. Instead, it tells
you which one to read at each stage and in what order:

- [Deployment Modes](DEPLOYMENT_MODES.md) — *which trust posture fits you*
- [Three Customer Workflows](VAULT.md#three-customer-workflows) — *how the Vault gets linked to your fault catalog and kept current*
- [Installation and Setup](../deploy/docker-compose/README.md) — *how to install the binaries*. This is specifically for Docker/Podman. For K8s, see [here](../deploy/helm/README.md). For VM/Bare Metal, see [here](../deploy/host/README.md). Beyond the official documentation, see these quick start guides for more color for [Docker/Podman](https://medium.com/@borisdali/aihelpdesk-quickstart-guide-for-docker-podman-ba67e9143d75), [K8s](https://medium.com/@borisdali/aihelpdesk-quickstart-guide-for-k8s-d9dae939557c) and [VM/Bare Metal](https://medium.com/@borisdali/aihelpdesk-quickstart-guide-on-a-vm-or-bare-metal-host-the-hard-way-5d09a7d2fdaa) respectively.

---

## 1. Pick a mode: don't start at "full write access"

Read [Deployment Modes](DEPLOYMENT_MODES.md) for the full comparison. Unless you're a
single engineer running this against your own sandbox (Mode 1 — Personal), start at
**Mode 2: Enterprise / IT-Hosted (Read-Only Governed)**. This is deliberate, not
conservative for its own sake. In Mode 2, mutation tools (`terminate_connection`,
`delete_pod`, etc.) are blocked **unconditionally in code**, not by a policy rule someone
could misconfigure. You get the full governance stack — audit trail, policy engine, role
checks, compliance snapshots — with no path to an accidental write, while you evaluate
whether the diagnoses are actually right for *your* infrastructure and *your* on-call
runbooks.

## 2. Point read-only mode at something real

Follow [deployment and setup guide](../deploy/docker-compose/README.md) §1.1 to
install (again, chose the guide appropriate for your deployment platform, see the links above). The config files you'll fill in, namely `infrastructure.json`, `policies.yaml`,
`users.yaml`, `.env`, all have `.example` counterparts to copy from; the demo's own
`deploy/docker-compose/demo/infrastructure.json` is a minimal reference for the schema.

Register a real target database. Ideally staging or a production replica or production itself
if you're confident in Mode 2's code-level mutation block. The goal of this stage isn't
remediation, it's **honest diagnosis on your actual incidents**: does the agent's
root-cause hypothesis match what your on-call engineers would have concluded? This is
also where [Right IX](CUSTOMER_RIGHTS.md#ix-the-right-to-the-original-claim) starts
earning its keep — every triage run now carries the alert that actually triggered it, so
you can compare it against what the agent concluded.

## 3. Let the Vault accumulate a real track record

This is the stage the demo can't show you, because the demo's numbers reset with the
volume. Yours won't. Every triage run in Mode 2 feeds
[the Flywheel](RIGHTS_AND_THE_FLYWHEEL.md) the same way the demo's did — except now
`vault accuracy` and `vault calibration` are measuring real incidents against your real
playbooks, not an injected fault repeated on a schedule.

Don't rush this stage. [Right IV](CUSTOMER_RIGHTS.md#iv-the-right-to-know-the-grade)
exists precisely so you don't have to take "it's ready" on faith — watch the resolution
rate and the diagnosis accuracy (once you've submitted feedback via `vault feedback`)
build up and only move to the next stage once the numbers say what you need them to say.
If a playbook's diagnosis accuracy is inconsistent, that's a signal to fix the playbook
[before](JUDGMENT_LAYER.md), not after, you hand it write access.

## 4. Graduate to write / destructive access

Move to **Mode 3: Enterprise Full** (see
[Deployment Modes](DEPLOYMENT_MODES.md)). This is where you configure:

- `HELPDESK_OPERATING_MODE=fix` (see [AI Governances guide §6](AIGOVERNANCE.md#6-operating-mode)
  for startup validation and runtime enforcement)
- Approval workflows — who approves what and through which channel (Decision Hub,
  Slack, git-branch) — see [AI Governance §4](AIGOVERNANCE.md#4-approval-workflows)
- Blast-radius guardrails (`max_rows_affected`, `max_pods_affected`,
  `max_xact_age_secs`) — see [AI Governance §5](AIGOVERNANCE.md#5-guardrails)
- `approval_override_roles` per target, if you want a `force` escape hatch for specific
  roles — this is exactly what the demo's Mode C (`DEMO_MODE=clamping`) demonstrates

Nothing about the gate changes at this point — see
[Informed Consent](INFORMED_CONSENT.md). What changes is that the gate now sits next
to a track record you built on your own incidents, not a demo's.

## 5. Ongoing operation

Two things keep this from decaying into a static tool:

- **Link your fault catalog to your Playbooks** — see
  [Vault §1, Onboarding](VAULT.md#1-onboarding--linking-your-first-playbooks) for the
  mechanical step (this is the narrower, later-stage "onboarding" the Vault docs refer
  to — a one-time linking task, not this whole page).
- **Run `faulttest` as regression monitoring** — see
  [Fault Injection Testing System](FAULTTEST.md), the customer-facing guide for validating agent behavior
  against your own staging database on an ongoing basis, catching regressions before
  they show up as a live incident.

---

## See also

| Doc | What it covers |
|---|---|
| [DEPLOYMENT_MODES.md](DEPLOYMENT_MODES.md) | The three trust postures in full detail |
| [deploy/docker-compose/README.md](../deploy/docker-compose/README.md) | Installation mechanics, both Docker and bare-binary routes |
| [RIGHTS_AND_THE_FLYWHEEL.md](RIGHTS_AND_THE_FLYWHEEL.md) | The loop this page's stage 3 is asking you to let run |
| [VAULT.md](VAULT.md#three-customer-workflows) | Playbook linking, draft review, regression monitoring |
| [AIGOVERNANCE.md](AIGOVERNANCE.md) | Operating modes, approval workflows, guardrails — the stage 4 configuration surface |
| [FAULTTEST.md](FAULTTEST.md) | Ongoing validation against your own infrastructure |
