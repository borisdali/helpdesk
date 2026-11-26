# aiHelpDesk: Is AI SRE Just Faster Incident Response?

No, and since faster troubleshooting and remediation is what every AI SRE tool for operations promises, the
claim needs a proper answer. This page gives ours: what we think an AI SRE for databases is for, what the
model does in aiHelpDesk and what it does not and where we are not claiming anything. It is the page to send
to someone who asks "how is this different?". The audience is in [Who aiHelpDesk is for](FOR_WHOM.md).

## 1/ Speed is the entry ticket

We aim to be fast too (beyond the obvious speed of fast log analysis, aiHelpDesk [Operational SRE/DBA Flywheel](VAULT.md#the-operational-sredba-flywheel) makes the troubleshooting and the resolution of a second occurrence of a similar failure scenario quicker than the first), but speed is not what we ask you to buy. For a database the harder questions are the ones a faster fix
does not answer, starting with whether or not a restore is the right answer (in favor of a failover for example), what will this restore throw away, can the business afford the downtime, was the backup ever
restored properly and can you show afterwards who agreed and what the numbers were. A fast restore that loses data nobody expected to lose is worse than a slow one that was carefully reviewed, deliberately chosen and approved with the [clear understanding of the consequences](INFORMED_CONSENT.md).

So we treat recovery as something to plan, price and check, before, during and after [an incident](INCIDENTS.md):

| When | The question | What aiHelpDesk does |
|------|--------------|----------------------|
| Before | What can the business afford? | You declare `rpo`, `rto` and an availability target; the [objectives](OBJECTIVES.md) and the downtime budget they imply are what every later figure is judged against. |
| Before | Does the backup restore and how long does it take? | A [restore drill](RESTORE_OPS.md) on a clone you bring is verified, recorded and counted in the restore-time estimate; an overdue one is reported. |
| During | What will this restore cost? | The plan shows the data it will discard, the time it is expected to take and the budget left and the approval gate is where a person says yes, with a justification. |
| After | Did it match the plan? | [Plan vs actual](PITR.md#plan-vs-actual) sets the recovery time, the data loss and the budget charge against the plan and the objectives in force. |
| Throughout | Can you prove it? | Every stage is a hash-covered event in the [audit trail](AUDIT.md). |

## 2/ So why AI? What the model does in all this?

It investigates (which tool to call, what the output means, which hypothesis the evidence supports, which
playbook comes next) and it proposes. It does not compute a figure, choose a recovery target, approve or
verify the result: those are code and people. See
[what the model does and what it does not](VS_YOUR_SCRIPTS.md#what-the-model-does-and-what-it-does-not) and
the [design principles](PRINCIPLES.md).

The reason for the split is that models are good at reading messy evidence and poor at exact arithmetic and at
holding to a safety rule every time. [Multiple layers](AIGOVERNANCE.md#11-llm-fabrication-detection) of safeguards exist in aiHelpDesk because the model can be wrong: a quoted piece of
evidence must appear in the real tool output and a restore is refused when the WAL chain is broken however
sure the model sounds. The model's judgment is not perfectly stable either, which is why it proposes and code
and people decide and why every release is tested by [fault injection](FAULTTEST.md).

**The model has no access of its own to your database, host or cluster and it does not write free SQL**  
Everything it does is a call to a tool in the [Tool Registry](TOOL_REGISTRY.md#principles): a closed catalog of
tools that are written, classified (`read`, `write` or `destructive`), tested and documented in advance.   

The model chooses which tool to call and with which arguments; the tool's code decides what runs. The policy engine gates
each call by its class, a `read` tool changes nothing whatever it is given and every mutating tool is written up
with how to undo it. The one place caller SQL reaches the database is `explain_query`, which accepts a single
statement and runs it in a transaction that is rolled back (see the limit in the registry's principle 5). So
"what can the AI do to my database?" has a short answer: only what is in the registry and you can read it.

**It shows its work, which most operations tools do not**   

For each diagnosis the operator sees   
  * the artifacts it reviewed: the tool calls and their output, all in the [audit trail](AUDIT.md)  
  * the hypotheses it formed, each with a confidence and a verbatim quote from the evidence  
  * why it rejected the others, one stated reason each, leaving a primary hypothesis and a root cause.   

The format and the checks are in [Structured diagnostic report](PLAYBOOKS.md#structured-diagnostic-report). A quote
that is not in the tool output is flagged and a diagnosis the model is not confident in is not allowed to run
on: below 70% confidence, or with a close competitor, the gate warns and suggests manual approval and below 50%
the gateway forces a human gate whatever was requested, so a coin-flip diagnosis never chains into a
destructive step on its own.

## 3/ What else sets it apart?

1. **Playbooks, not runbooks**   
   A runbook is a fixed sequence of steps written once for the environment of that
   day and it is out of date as soon as the platform, the version or the tooling moves, which today is
   constantly.   

   A [playbook](PLAYBOOKS.md) records the *intent* and the expert knowledge instead: what class of
   problem this is, what its symptoms are, what to prioritise and when to stop and ask a person. The steps are
   worked out each time from the tools and the infrastructure that exist then, so one playbook handles the same
   fault differently on a host and in a K8s Pod.   

   It is close to what K8s did for operations:
   you declare *what* should hold and the system works out *how*, instead of scripting every step (the analogy is
   about how it is written, a playbook does not reconcile continuously). And you can see it: the playbooks are
   human-readable YAML you own ([Right VII](CUSTOMER_RIGHTS.md#vii-the-right-to-own-your-knowledge)),
   versioned and reviewed like code and every run records the steps it took.

2. **Objectives decide what matters, the model finds a path that respects them**   
   You declare `rpo`, `rto` and the availability target; the model does not set or change them.   

   What it brings is the part a fixed script
   cannot: working out, from a live and untidy incident, which situation this is (a crash or a clean stop, a
   lost data directory or a bad query, a backup that can be restored to the end of the archive or one that
   cannot) and so which recovery path the objectives should be applied to, then explaining that path with
   quoted evidence for the person who approves it. The arithmetic that judges the path against the objectives
   stays in code.
3. **The same approach on Docker/Podman, a host or VM and K8s**   
   The playbooks do not change between the platforms. The tools the agents use do ([PLATFORMS.md](PLATFORMS.md#3-what-aihelpdesk-can-do-per-target)).
   The agent-driven restore is verified on a Docker container, on an Ubuntu VM over SSH and on a self-managed
   pgBackRest Pod in K8s.

4. **Drills, with a reminder and a number**   
   aiHelpDesk [does not run a test restore](https://itnext.io/your-ai-wont-run-your-fire-drills-but-it-will-check-that-you-did-c20156397868) for you. 

   It records a restore you ran on a clone, verifies the clone against the source, reminds you when the last verified drill is
   older than your `drill_interval` (a warning, nothing is blocked) and uses what the drill measured to
   estimate the real restore until the server has three real ones, labelled "demonstrated on a copy".   

   A drill reads shorter than a real restore, so that estimate is a lower bound and is shown as one, see more in [Restore operations](RESTORE_OPS.md).

## 4/ "We already have tested scripts and runbooks"

This is a common objection and a totally valid one.   

The thing is that aiHelpDesk doesn't change the mechanics of a restore, for instance. A pgBackRest restore is a pgBackRest restore. What a script usually does not do is everything around
it (see also [this sample transcript](samples/SAMPLE032.md) for details): say what it will throw away before it runs, know whether you can afford the downtime, refuse itself when
you cannot, record who agreed and what the numbers were and get better at predicting its own duration from
this server's history.   

A shop that already measures its restore times, ties approvals to the downtime budget (e.g. like [this](PITR.md#the-downtime-budget-at-the-gate))
and keeps the evidence has built much of this by hand. We make it the default, the standard, so it does not depend on one
person staying. The questions to put to your own automation and where we are not better, are in
[aiHelpDesk and the automation you already have](VS_YOUR_SCRIPTS.md), with the short version in
[Who aiHelpDesk is for §6](FOR_WHOM.md#6-we-already-have-scripts-and-automation-what-would-we-gain).

## 5/ "Is it just about backup and recovery?"

No, and it is not a general-purpose database agent either
([§3 of Who aiHelpDesk is for](FOR_WHOM.md#3-is-the-next-step-a-general-purpose-database-agent)). The
[fault catalog](FAULTTEST.md) covers connections, locks, replication, vacuum, disk and WAL pressure,
configuration, K8s and host failures, with approval-gated fixes. Recovery is where we go deepest:
the objectives, the budget, plan vs actual and drills apply to restore and point-in-time recovery today and
failover and disaster recovery are planned.

## 6/ What we do not claim?

- **We do not run your drills**   
  aiHelpDesk checks that you did and tells you when you have not. A drill
  today exercises a restore of a backup; it is not a failover test.

- **The figures need objectives**   
  Without a declared `rpo`, `rto` and availability target there is nothing
  for the plan and the verdicts to be judged against.

- **Failover and disaster recovery are not built yet**   
  They are planned, judged the same way (see
  [Who aiHelpDesk is for §3](FOR_WHOM.md#3-is-the-next-step-a-general-purpose-database-agent)).

- **The model is not infallible**   
  That is the reason it proposes and code and people decide.

**Faster response is the entry ticket. Recovery you can plan, price, practice and defend afterwards is what the accountability layer is for.**

## See Also

| Document | What it covers |
|----------|----------------|
| [FOR_WHOM.md](FOR_WHOM.md) | Who aiHelpDesk is for, whether it replaces DBAs and SREs and where it fits in the AI landscape |
| [VS_YOUR_SCRIPTS.md](VS_YOUR_SCRIPTS.md) | What a restore script, runbook or operator already gives you and what aiHelpDesk adds; where it does not help |
| [PRINCIPLES.md](PRINCIPLES.md) | Design principles, including which layer reasons and which layer is deterministic |
| [OBJECTIVES.md](OBJECTIVES.md) | The recovery objectives, the downtime budget and how a plan is judged against them |
| [RESTORE_OPS.md](RESTORE_OPS.md) | Restore drills and reading the plan and the plan vs actual |
| [PLATFORMS.md](PLATFORMS.md) | What runs where and what is verified |
