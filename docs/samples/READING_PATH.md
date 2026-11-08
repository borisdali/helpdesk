# Reading path through the aiHelpDesk blog series

The [samples index](README.md) is a *subset* of all aiHelpDesk blog posts that have a corresponding raw transcript (with the sample commands and their output). It is ordered chronologically, by publication date, which is the wrong order for someone new to aiHelpDesk, arriving here just now. So this page groups the same posts by the question they answer, so you can start where your own question is.

The *complete list* of posts (which includes posts with no transcript) is on [aiHelpDesk.biz](https://aiHelpDesk.biz). This page links only the posts the samples index lists, so a post you know of may be missing here.

- [If you read only three](#if-you-read-only-three)
- [Who is who](#who-is-who)
- [The arcs](#the-arcs)
  - [1. Should the AI SRE/DBA be allowed to act and who says so?](#1-should-the-ai-sredba-be-allowed-to-act-and-who-says-so)
  - [2. Can you prove what AI SRE/DBA did and why?](#2-can-you-prove-what-ai-sredba-did-and-why)
  - [3. Is it getting better or just claiming to?](#3-is-it-getting-better-or-just-claiming-to)
  - [4. How do you evaluate it without trusting its own report?](#4-how-do-you-evaluate-it-without-trusting-its-own-report)
  - [5. What does an escalation actually cost?](#5-what-does-an-escalation-actually-cost)
  - [6. What happens when it lies or when the evidence is wrong?](#6-what-happens-when-it-lies-or-when-the-evidence-is-wrong)
  - [7. Backups and restores, the most destructive thing it does](#7-backups-and-restores-the-most-destructive-thing-it-does)
- [How to use the samples](#how-to-use-the-samples)

## If you read only three

1. [You let AI Operate on Production Database Without Your Consent?](https://medium.com/google-cloud/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266) (#13): the premise. The agent diagnoses and shows its plan, a human oeprator says yes.
2. [AI Fixed Your Database. Here Are the Receipts](https://tinyurl.com/bdf9hpwy) (#16): what makes "it said it fixed it" checkable afterwards.
3. [AI-Driven Restore That Stops Before the Mistake](https://levelup.gitconnected.com/ai-driven-restore-that-stops-before-the-mistake-8e7ff9ca8064) (#49): the same two ideas applied to the hardest operation, where data is deliberately discarded.

Two documents in the repository state the same positions without the stories: the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md) and the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel) and the third doc explaining [how the two relate](../RIGHTS_AND_THE_FLYWHEEL.md).

## Who is who

The people in the stories are narrative devices. The commands, queries and output in the sample files are from real runs.

| Name | Role in the stories | Posts |
|---|---|---|
| Michael | the on-call engineer whose trust in the pages is earned step by step | #32, #33, #37 |
| Dana | evaluates vendor demos and does not let the builder showcase the product | #38 |
| Sam | reads a vendor's databases, not its documentation | #39 |
| Dan | the DBA with ten years of respect for the restore command | #46, #47, #49, #50 |
| Martin | runs about forty production databases, half in Pods and half on VMs | #48 |

## The arcs

### 1. Should the AI SRE/DBA be allowed to act and who says so?

- #12 [Your AI Just Diagnosed the Outage. Should It Fix It Too?](https://medium.com/google-cloud/your-ai-just-diagnosed-the-outage-should-it-fix-it-too-ff6a43ec475f): a person at every boundary between knowing and doing.
- #13 [You let AI Operate on Production Database Without Your Consent?](https://medium.com/google-cloud/you-let-ai-operate-on-production-database-without-your-consent-bd4ffb954266): informed consent for the 2 a.m. incident.
- #26 [Asking Your User to Click OK isn't Trust. Stop Apologizing for the AI Approval Gate](https://itnext.io/noisy-neighbors-not-bad-tenants-can-your-ai-sre-tool-tell-the-difference-47bb49d2dd1e): why the gate is a feature.

### 2. Can you prove what AI SRE/DBA did and why?

- #14 [You got informed consent. Can you prove the AI was right?](https://medium.com/google-cloud/you-got-informed-consent-can-you-prove-the-ai-was-right-f62bda72d602)
- #16 [AI Fixed Your Database. Here Are the Receipts](https://tinyurl.com/bdf9hpwy): the audit trail as a ledger.
- #19 [“The AI Said So”. Blind Trust Is Not a Feature. It’s Liability](https://levelup.gitconnected.com/the-ai-said-so-blind-trust-is-not-a-feature-its-liability-ce5d12e03d27): confidence scores with no denominator.
- #23 [Trust Has Three Dimensions. Demand from your AI SRE vendor to show a cert with all three.](https://itnext.io/trust-has-three-dimensions-demand-from-your-ai-sre-vendor-to-show-a-cert-with-all-three-0f361a443807)
- #32 to #37, Michael's three posts, are the same question over time: [Show Your Work](https://itnext.io/show-your-work-how-the-page-stopped-being-a-guessing-game-0e80fa3f35c9), [The AI that Un-Trusts Itself](https://itnext.io/the-ai-that-un-trusts-itself-the-page-that-never-came-6cc9df173533) and [The Last One That Still Needed Him](https://itnext.io/the-last-one-that-still-needed-him-14af8f324776).

### 3. Is it getting better or just claiming to?

- #17 [AI SRE Just Got Its First Report Card](https://tinyurl.com/yc57bywd): turning the audit trail into a learning curve.
- #18 [Here’s what happened when AI SRE tried to improve its own playbook](https://levelup.gitconnected.com/ai-to-take-over-your-job-heres-what-happened-when-ai-sre-tried-to-improve-its-own-playbook-c7a6338fae37): the unhappy path of the flywheel.
- #22 [Your AI Just Rewrote Its Own Playbook. How Do You Know It Got Better?](https://levelup.gitconnected.com/your-ai-just-rewrote-its-own-playbook-how-do-you-know-it-got-better-85322fdec17c)

### 4. How do you evaluate it without trusting its own report?

- #15 [The LLM Is the Dumbest Part of Your AI SRE Platform](https://tinyurl.com/677by4ut): why the architecture must not depend on one model.
- #20 [AI Benchmark Said It Was Better. Your Production Database Disagreed](https://levelup.gitconnected.com/ai-benchmark-said-it-was-better-your-production-database-disagreed-21fb9853d8a9)
- #38 [AI-Based DB Replication Triage and Remediation. The Evaluation Story](https://itnext.io/ai-based-db-replication-triage-and-remediation-the-evaluation-story-140cef5f5677) (Dana)
- #39 [How to evaluate and audit a vendor AI-powered product?](https://levelup.gitconnected.com/how-to-evaluate-and-audit-a-vendor-ai-powered-product-dfb06400334d) (Sam)
- #41 [Stop Trusting AI SRE and AIOps Tools That Grade Their Own Homework](https://itnext.io/stop-trusting-ai-sre-and-aiops-tools-that-grade-their-own-homework-5ca7660ebf24)

### 5. What does an escalation actually cost?

- #29 [The Hand-Off Tax, Part 1: What a 3-Hop Escalation Chain Actually Buys You](https://levelup.gitconnected.com/the-hand-off-tax-part-1-what-a-3-hop-escalation-chain-actually-buys-you-d708f4912d4c)
- #30 [The Hand-Off Tax, Part 2: We Turned Off the Playbooks and Watched](https://itnext.io/the-hand-off-tax-part-2-we-turned-off-the-playbooks-and-watched-fade1e510391)

### 6. What happens when it lies or when the evidence is wrong?

- #9 [AI Database Troubleshooting: the PostgreSQL Stat That Looks Like Good News (But Ain’t)](https://medium.com/google-cloud/ai-database-troubleshooting-the-postgresql-stat-that-looks-like-good-news-and-aint-16f1b52143ec): reading data is not understanding it.
- #11 [Your AI troubleshooted and “fixed” a database pileup. The locks didn’t care](https://medium.com/google-cloud/ai-troubleshooted-db-pile-up-and-reported-success-the-locks-didnt-care-d8c06faded82)
- #40 [The Corruption That Wasn’t There](https://itnext.io/the-corruption-that-wasnt-there-138085295c1b): catching a fabricated finding, told through the SQL that caught it.
- #42 [Your AI Agent Has No Alibi and It Just Lied to You. Stop Grading It on Whether It Finished the Task](https://levelup.gitconnected.com/your-ai-agent-has-no-alibi-and-it-just-lied-to-you-stop-grading-it-on-whether-it-finished-the-task-7efe31b5ba7a)

### 7. Backups and restores, the most destructive thing it does

Read these in order; each one builds on the previous.

- #44 [When the Watchman Can’t See: How a Silent Backup Failure Almost Stayed Silent Forever](https://itnext.io/when-the-watchman-cant-see-how-a-silent-backup-failure-almost-stayed-silent-forever-98ccc9597043): noticing the backup job has stopped working.
- #46 [Your DBA’s Scariest Command Now Runs Itself](https://itnext.io/your-dbas-scariest-command-now-runs-itself-e4c6d161d590) (Dan): the in-place restore.
- #47 [Postgres Never Asks for RESETLOGS](https://itnext.io/postgres-never-asks-for-resetlogs-d313f6801ac5): proving a restore has no hole in its history before overwriting anything.
- #48 [Break It on a Tuesday](https://itnext.io/break-it-on-a-tuesday-e3113b17bf8d) (Martin): rehearsing the recovery in your own estate, pods and VMs.
- #49 [AI-Driven Restore That Stops Before the Mistake](https://levelup.gitconnected.com/ai-driven-restore-that-stops-before-the-mistake-8e7ff9ca8064) (Dan): a point-in-time restore, where losing data is the plan.
- #50 (Dan and Martin): what the restore would discard, measured against an objective, with the decision recorded. 

## How to use the samples

Every sample file is a raw transcript: the commands that were run, the output and the deliberation, so you can compare it with what your own environment does. The platform column in the [index](README.md) says whether a sample ran from source, inside Docker/Podman containers, on a host/VM or on K8s.
