# aiHelpDesk and the automation you already have

"We already have scripts for that" is the most reasonable objection to aiHelpDesk and for the mechanics of a restore it is right. A
`pgbackrest restore` is a `pgbackrest restore`, whoever starts it and a good script is as fast. This page is about what a script or a
runbook usually does **not** do, what aiHelpDesk adds in the path of the same action and where it does not help.

## Seven questions to ask of your own automation

Take the script or the runbook you would use to restore a production database to a point in time and ask:

| # | Does it... | What aiHelpDesk does | Where to see it |
|---|---|---|---|
| 1 | **Say what it will throw away, before it runs?** | The plan reports the history that would be discarded and counts the transactions committed after the target, by decoding the WAL (`pg_waldump`). When it cannot count, it says why and gives a lower bound. It never reports "nothing lost" for a count it could not make. | [Sample#29](samples/SAMPLE029.md), [PITR.md §4](PITR.md#4-operating-it) |
| 2 | **Know whether you can afford it?** | The plan sets the loss against your declared `rpo` and the downtime against the budget your `availability_slo` allows, less what recorded incidents have already spent. | [OBJECTIVES.md §5](OBJECTIVES.md#5-reading-it-in-code), [Sample#30](samples/SAMPLE030.md) |
| 3 | **Refuse itself?** | When the plan says an acknowledgement is required and the approval lacks it, the restore tool refuses **before it stops the instance**. The check is in the tool, not in a prompt, so a model's mistake or a rushed operator cannot skip it. A gate can be approved once; the refusal says to start again. | [PITR.md §4](PITR.md#4-operating-it), [Sample#31](samples/SAMPLE031.md) |
| 4 | **Record who agreed and what the numbers were?** | Each stage of the decision is written to the audit trail as a hash-covered `recovery_decision`: who approved, the objectives in force with where each came from, the plan's digest. | [AUDIT.md §4.6](AUDIT.md#46-recovery_decision-fields) |
| 5 | **Learn from its own history?** | The recovery-time estimate comes from this server's real past restores (nothing below three, a median and the longest from three, a p90 from ten) and replaces the declared `rto` in the budget check. The ledger shows what is spent. Fault-injection runs never count. | [OBJECTIVES.md §5.3 and §5.5](OBJECTIVES.md#55-the-recovery-time-estimate) |
| 6 | **Prove where the restore stopped?** | After the restart, the recovery is checked against Postgres own log lines (the stop line, the redo position, the new timeline) and reported `verified` or not. | [PITR.md §6](PITR.md#6-checking-where-recovery-stopped) |
| 7 | **Behave the same on every platform?** | The same flow ran on a Docker container, on a systemd VM over SSH and on K8s. | [PLATFORMS.md](PLATFORMS.md) (the verified-runs table says which part ran where) |


**If your automation already answers all seven, you do not need aiHelpDesk for restores**   
In our experience in the field, most do not answer more than two or three and the missing ones tend to be the ones an incident review asks about afterwards: what did it cost and who said yes?
  
(also consider reviewing our take on a difference between the *runbooks* and *playbooks*, see [here](https://medium.com/google-cloud/runbooks-rot-playbooks-learn-bec433817938?) an [here](PLAYBOOKS.md)).

## What the model does and what it does not

The model is not in charge of any of the above. It does not choose the recovery target, give the approval, decide whether the budget
allows the restore or compute any figure. Those are code: the plan, the gate, the budget arithmetic and the tool's own refusals.
A person supplies and approves the target.

What the model does is the work around the restore: reading the situation (is the instance up, is the plan feasible), following the
playbook's steps, handling what a script would stop on (for example, reading the Postgres log directly when the service journal
has no database lines) and writing the narrative of the incident.

It also gets things wrong and we have seen it:

- calling a tool with a wrong parameter name before correcting itself,
- reporting a refusal in its own words, with advice that was stale,
- quoting loosely when asked for evidence.

The design assumes this. The decisions that matter sit in tools and gates that refuse regardless of what the model says and a quote
that is not in the tool's output is flagged as unverified evidence on the incident
([MUTATION_TOOLS.md §5.11](MUTATION_TOOLS.md#511-content-provenance-verification-checkevidenceprovenance)).

## What it costs you

Time. A script has no triage, no model and no gate. In our runs the recovery itself on the tiny databases took 33 to 101 seconds, depending on the server,
and the incident from start to finish about 90 to 120 seconds. The difference is the triage, the model's reasoning and the wait for
the approval and that wait grows with how long a person takes. See the "Two clocks" note in
[OBJECTIVES.md §5.5](OBJECTIVES.md#55-the-recovery-time-estimate): neither figure is the downtime and the gate compares them.

## How it fits with what you have

aiHelpDesk drives your tooling. It does not replace it. The restore is `pgbackrest restore`, the status is `pgbackrest info`, the
service control is `systemctl` or the container runtime or `kubectl`. You still need pgBackRest, your operator or Patroni and whatever
takes your backups on a schedule. aiHelpDesk acts when there is an incident or a person asks, through a gate, within the objectives
you declared ([who this is for, section 3](FOR_WHOM.md#3-is-the-next-step-a-general-purpose-database-agent)).

Instead of replacing your current tooling, aiHelpDesk makes it adaptable, flexible and multiplatform. aiHelpDesk amplifies your existing investment in building and mastering your tools rather than wasting it.

