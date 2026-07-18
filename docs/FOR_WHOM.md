# Who aiHelpDesk Is For

This document answers three questions that come up repeatedly from people evaluating
aiHelpDesk, commenting on our blog posts or comparing us to horizontal AI platforms:

1. [Who is the target audience?](#1-who-this-is-for)
2. [Does aiHelpDesk replace DBAs and SREs?](#2-does-aihelpdesk-replace-dbas-and-sres)
3. [Is the next step a general-purpose Database Agent?](#3-is-the-next-step-a-general-purpose-database-agent)
4. [Where aiHelpDesk fits in the AI landscape](#4-where-aihelpdesk-fits-in-the-ai-landscape)
5. [Why now, not later?](#5-why-now-not-later)

---

## 1. Who This Is For?

**aiHelpDesk is for operations teams who are already using or seriously considering, AI
agents for database and K8s incident response. Agents that are strictly and unequivocally
accountable, not just capable.**

The target roles are:

- **DBAs and SREs** who are on-call for production PostgreSQL and K8s systems and
  who want AI-assisted triage and remediation without giving up visibility into what the
  agent concluded, why and whether it has proven consistent.
- **Platform engineers** who are deploying AI agents inside an organization where
  someone — a compliance team, an incident review board, a VP of Engineering — will
  eventually ask: "what did the AI decide, who approved it and how often is it right?"
- **Engineering leaders** who have evaluated horizontal AI platforms for incident
  triage and found that "similar quality" is a claim that doesn't survive contact with
  a postmortem.

aiHelpDesk is not for teams who want AI to help write migrations, optimize schemas,
design indexes or answer general SQL questions. Those tools exist and are
well-served by general-purpose agents. aiHelpDesk is for the moment something goes
wrong in a production system and an AI is about to recommend a write or destructive
action.

**The distinction that matters:** a _productivity_ agent improves your team's throughput.
An _accountability_ agent certifies that what was done was correct, safe and defensible.
Those are different products, different architectures and different buyer moments.
aiHelpDesk is the second kind, exclusively.

---

## 2. Does aiHelpDesk Replace DBAs and SREs?

No. And the framing of the question is also not right. Here's why:

Not every shop has DBAs and DBA teams. If yours does, it's likely that you are an
enterprise customer with the decent number and sizes of your database fleet.
If so, DBAs typically play important, often critical, role in your overall IT organization. 
Whether it's being part of the design, development or production support for the 
mission critical databases, DBA's job is usually vital to ensuring that data is safe,
secured and sound.

Now, being paged for the 2am incidents is typically not the most pleasant part of the job.
Making your DBAs make critical decisions at this time is often not the best recipe
for keeping your data safe and sound, nor does it help your DBAs been lucid the next
day.

Offload that function. Incident management, real and proactive is better handled by AI.
But not any AI. Responsible and accountable AI. AI that you trust. Not blindly trust,
but trust based on the previous track record. Based on transparency. Based on the fact
that you can review that AI-based system's code (because it's OSS).

Not blind trust. Trust that's been earned. Earned through testing and track record, see
the [Bill of Rights](CUSTOMER_RIGHTS.md).

> aiHelpDesk is DBA and SRE trusted partner. Not a replacement. With aiHelpDesk, your
> operational staff can focus on the tasks that are important to the business, not toil.

aiHelpDesk doesn't make the role of a DBA obsolete. There's no replacement of sound human
judgement, see [here](JUDGMENT_LAYER.md) for in depth coverage of this point, but TL;DR is
this:
there is a specific class of decisions — knowing which recommendations to prohibit and
why, encoding those prohibitions as CRITICAL paragraphs in a playbook, measuring whether
the prohibitions hold under repeated fault injection — that requires operational
experience the AI _cannot synthesise from incident traces alone_. Not in principle, in
practice: the trace does not contain the knowledge that one recommendation belongs to a
different time horizon than another and that encoding the distinction as a prohibition
is more effective than encoding it as elaboration.

As such, we propose a different framing of this question: **which parts of DBA and SRE work compound into better AI and
which parts require irreplaceable human judgment?**

From the aiHelpDesk [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel):

- The AI handles pattern recognition at scale: across 20 runs of the same fault, it
  identifies failure modes faster than a human reviewing 20 incident reports and
  synthesises proposed fixes without requiring anyone to write a script to extract the
  signal.
- The DBA handles the judgment layer: knowing the specific trap by name, writing the
  prohibition that forecloses it and measuring over subsequent runs whether the
  prohibition held.

Every CRITICAL paragraph in a [playbook](PLAYBOOKS.md), every explicit prohibition, every decision table
that eliminates a branch point — those were written by someone who understood the failure
mode at a level the AI could not reach from traces alone. That knowledge compounds. The
flywheel works when the judgment layer activates infrequently but decisively and when
the DBA's operational experience is encoded in the playbook as durable prohibition rather
than tribal memory that walks out the door.

**The self-driving SRE is not the right goal. The accountable SRE, whose AI
co-pilot can be audited, certified and corrected when wrong, is.**

---

## 3. Is the Next Step a General-Purpose Database Agent?

No. Explicitly not.

The roadmap goes deeper on governance and certification, not broader on database
management capability.

**What "deeper" means concretely:**

- More fault scenarios in the [catalog](FAULTTEST.md) (currently 32; targeting coverage
  of every class of production failure a DBA or SRE encounters in the first year of
  on-call).
- More attribution dimensions in the [stability cert](ATTRIBUTION_CERTS.md): the v0.21.0
  cert is three-dimensional (outcome, conclusion, evaluation); the roadmap adds
  per-model comparison and per-taxonomy-version tracking so a model upgrade is a
  measurable event, not an implicit replacement.
- Stronger calibration: the [vault calibration](VAULT.md#vault-calibration) band today
  reflects a blend of human feedback and LLM self-consistency; the target is a cert
  where the human feedback fraction is visible, queryable and growing.
- More governance surface: blast-radius enforcement for Kubernetes, host-level sysadmin
  operations and cross-domain failure propagation reasoning.

**What "broader" would look like — and why we're not going there:**

A general-purpose database agent would add DDL tools (CREATE TABLE, ALTER TABLE),
query optimisation recommendations, index suggestions, migration authoring, schema design
assistance. Those features exist in other products: pganalyze, Neon AI, PlanetScale
Insights and every AI-native database startup currently pitching "talk to your database."
Competing in that space would require aiHelpDesk to be a different product — one that
prioritises breadth of database management capability over depth of operational
accountability.

aiHelpDesk's moat is not in agent capability. Foundation models are improving fast enough that a
general-purpose agent with `psql` access and a good prompt can approximate triage quality
within a reasonable range. The moat is the [governance](AIGOVERNANCE.md) and [calibration](VAULT.md#vault-calibration) layer that makes
the agent's conclusions certifiable: the [fault catalog](FAULTTEST.md), the [attribution taxonomy](ATTRIBUTION_CERTS.md#2-how-attribution-classification-works), the
evaluation data, the [audit trail](AUDIT.md), the [step-approval gate](PLAYBOOKS.md#informed-gate). That layer cannot be replicated
by a horizontal platform without making the same investment we've already made in the
specific domain of production database and K8s operations.

Going broad would trade that depth for a larger apparent TAM and a much weaker
competitive position. We are not doing that.

---

## 4. Where aiHelpDesk Fits in the AI Landscape

Horizontal AI platforms (Replit, internal agent platforms, general-purpose code/ops
agents) are building the productivity layer: more code shipped, faster review cycles,
automated support ticket triage. That's real value and we don't compete with it.

aiHelpDesk is building the accountability layer for the subset of that work where
"similar quality" is not an acceptable standard: production database writes, Kubernetes
pod terminations, connection kills, deployment restarts. Actions where the question is
not "did the agent do something useful" but "can I defend what the agent decided, to a
post-mortem audience, at 3am."

The two layers are complementary. An engineering team can use a horizontal agent to move
faster on everything and use aiHelpDesk to ensure that the AI-assisted operations on
their most critical systems are auditable, calibrated and certifiably consistent.

**The productivity agent makes your team faster. aiHelpDesk makes your production
operations defensible.**

---

## 5. Why Now, Not Later?

The value of aiHelpDesk compounds from the day you start. Here is why the adoption
decision is time-sensitive, not evergreen.

**The governance gap is opening now, not in the future.** AI agents are being deployed
for incident response across engineering organisations today. Not as a pilot, as standard
practice. In most cases these deployments have no audit trail, no step approval gate, no
blast-radius check before a destructive action and no cert proving the agent was right the
last five times it saw this fault class. The first serious post-mortem that asks "what did
the AI decide, who approved it and how do we know it was right?" will find nothing to show.
The governance infrastructure that answers those questions needs to exist before the incident,
not after.

**The calibration data compounds.** A [stability cert](ATTRIBUTION_CERTS.md) backed by 3
runs means something. One backed by 30 means materially more. The
[fault catalog](FAULTTEST.md), the evaluation data, the attribution history — these grow
with every run through the [flywheel](VAULT.md#the-operational-sredba-flywheel). A team
that starts today has a 6–12 month head start on calibration depth over a team that starts
next year. That gap shows up directly in the cert: `STABLE(3)` vs. `STABLE(30)` is a
different standard of proof and the difference is not something you can compress by running
tests in a burst. Each run requires a real fault injection, a real agent diagnosis and a
real judge evaluation. You cannot backfill operational history.

**Incidents keep happening.** Every production incident between now and adoption is
diagnostic data that goes unrecorded, operational knowledge that stays in Slack threads
rather than playbooks and a MTTR that could have been minutes instead of 30–60 minutes.
The cost is not dramatic. It accrues quietly. A team that has absorbed that cost for
three years and then starts measuring it finds it was larger than expected.

**Regulatory pressure has a deadline.** AI governance requirements under DORA, SOC 2
controls on automated decision-making and financial services regulations on AI-assisted
operations are arriving with specific implementation timelines, not open-ended horizons.
Building the audit trail after an external deadline is harder and more expensive than
building it into the operational workflow from the start.

---

## See Also

| Document | What it covers |
|----------|----------------|
| [JUDGMENT_LAYER.md](JUDGMENT_LAYER.md) | The three categories of irreplaceable DBA/SRE judgment; the manual improvement path when AI proposals regress |
| [CUSTOMER_RIGHTS.md](CUSTOMER_RIGHTS.md) | Ten commitments aiHelpDesk makes to operators; the mechanisms that back each one |
| [CONSISTENCY.md](CONSISTENCY.md) | The Triage Consistency Certificate; how stability is measured and what the three dimensions mean |
| [ATTRIBUTION_CERTS.md](ATTRIBUTION_CERTS.md) | Attribution-aware stability certs; outcome, conclusion and evaluation stability |
| [VAULT.md](VAULT.md) | The Operational SRE/DBA Flywheel; how incidents become calibration data |
| [PRINCIPLES.md](PRINCIPLES.md) | Design principles, including model-neutral design and bounded probabilism |
