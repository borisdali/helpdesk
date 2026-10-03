# aiHelpDesk Sample#22 (on K8s): The Bugs Only a Real Cluster Could Show

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[When the Watchman Can’t See: How a Silent Backup Failure Almost Stayed Silent Forever](https://itnext.io/when-the-watchman-cant-see-how-a-silent-backup-failure-almost-stayed-silent-forever-98ccc9597043)**  
  Your dashboard was green and yet your backups had been dead for two weeks. Nobody lied to you, but nobody asked the right question either  
  
  
If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely on K8s. Not because the capabilities shown here are new or special (all three were already built and live-verified against Docker), but because K8s is where every one of the real bugs in this transcript was actually found. See [here](SAMPLE010.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for VM/Bare Metal, the source and container (Docker/Podman) sample transcripts respectively.

This sample transcript is meant to showcase the three new capabilities in introduced in the [v0.30 release](https://github.com/borisdali/helpdesk/releases/tag/v0.30.0). The samples are of real runs against the live K8s cluster. Not `helm template`, not a unit test, not a mock:

- **Part A** — [Postgres-backend auditd](../AUDIT.md#82-postgres-backend): auditd's own storage running on a dedicated Postgres instance instead of SQLite.
- **Part B** — [WAL-archiving failure triage + remediation](../BACKUP.md#1-wal-archiving-failure-the-backup-taking-precondition): `get_backup_status`/`set_archive_command`.
- **Part C** — [pgBackRest job-level backup health](../BACKUP.md#2-pgbackrest-job-level-backup-health): `get_pgbackrest_status`/`run_pgbackrest_backup`, including a brand-new `kubectl exec` dispatch path.

Every one of Parts A-C had already passed its own Docker-based live verification. None of the six real findings below were ever caught there — not because Docker testing was sloppy, but because each one's root cause is something Docker genuinely does not do: auto-inject Service-derived environment variables into every Pod, enforce a read-only `postgresql.auto.conf` the way an operator-managed cluster does or make a brand-new `helm upgrade` interact with a years-old release's stored values. Two of them were only confirmed after a first, plausible-sounding hypothesis turned out to be wrong — caught before being shipped as the fix, not after.

---

## 1. Part C: pgBackRest — SSH vs. `kubectl exec` and the bug only K8s could trigger

For this test we use a self-managed (non-operator) Postgres+pgBackRest Pod and the exact same image already built for the Docker-based SSH work. [Here's](../../testing/k8s/pgbackrest-demo.yaml) the mainfest for creating a statefulset and a corresponding service (both named `pgbackrest-demo`):

```
$ kubectl apply -f testing/k8s/pgbackrest-demo.yaml
service/pgbackrest-demo created
statefulset.apps/pgbackrest-demo created

$ kubectl -n helpdesk-test exec pgbackrest-demo-0 -- su postgres -c 'pgbackrest --stanza=main --type=full --log-level-console=info backup'
...
2026-10-01 19:36:18.721 P00   INFO: backup command end: completed successfully (6723ms)
```

There are two rather different dispatch paths to reach the Pod. The first path is via **SSH**, while the other path is via a `kubectl port-forward` tunnel:

```
$ ssh -i testing/docker/ssh/pgbackrest_test_client_key \
  -o UserKnownHostsFile=/tmp/pgbackrest_k8s_known_hosts \
  -p 16436 postgres@localhost 'pgbackrest info --output=json'
[{"archive":[...],"backup":[{...,"label":"20261001-193612F",...}],...,"status":{"code":0,"message":"ok"}}]
```

**`kubectl exec`**, with zero port exposure at all — the dispatch this session added to `agents/sysadmin/tools.go`, reusing `execInProcess`'s existing K8s branch (already shared with `check_host`/`restart_container`):

```
$ kubectl -n helpdesk-test exec pgbackrest-demo-0 -- su postgres -c 'pgbackrest info --output=json --stanza=main'
P00   WARN: environment contains invalid option 'demo-service-port'
P00   WARN: environment contains invalid option 'demo-port-5432-tcp-proto'
...
[{"archive":[...]}]
```

**Why there are two paths to consider and how to choose one over the other?***  
The answer to "SSH vs. `kubectl exec`" is deceptively simple: SSH needs a reachable port, even if only via a tunnel and not everyone is comfortable with exposing their database Pod to the outside world (well, even if "outside" may just mean outside of the K8s cluster, but still within the corporate network). Now `kubectl exec` needs none at all, tunneling through the K8s API server using the same `pods/exec` RBAC every other sysadmin tool already relies on. But here's the point that is not so obvious: `kubectl exec` inherits the Pod's *entire* environment, including whatever K8s injects automatically, while SSH opens a new clean login shell and never sees any of it.

Those `WARN:` lines are K8s auto-injecting a `<SERVICE_NAME>_*` environment variable per Service in the namespace (the Service here is literally named `pgbackrest-demo`) and pgBackRest warning about each one that resembles its own `PGBACKREST_*`-prefixed options. The agent's own JSON parser choked on it:

```
"error": "get_pgbackrest_status: parsing pgbackrest info JSON: invalid character 'P' looking for beginning of value"
```

The first fix attempt was wrong and caught before shipping. The plausible story: `execRunner.Run`'s `cmd.CombinedOutput()` merges stdout and stderr, so the WARN noise must be bleeding in from stderr. Fixed `execRunner.Run` and `sshRun` to capture the two streams separately, returning stdout alone on success — a real improvement, kept regardless. Re-tested live:

```
$ kubectl -n helpdesk-test exec pgbackrest-demo-0 -- su postgres -c 'pgbackrest info --output=json' 2>/dev/null | head -3
P00   WARN: environment contains invalid option 'demo-service-port'
```

Still there — on *stdout*, checked directly rather than assumed a second time. pgBackRest writes its own warnings to stdout, ahead of the real JSON, on an otherwise perfectly healthy run. The actual fix, `jsonArrayPrefix` (`agents/sysadmin/tools.go`): locate the first `[` before unmarshaling, rather than assuming the whole string is JSON. Verified against the real Pod with the noise deliberately left in place, not worked around:

```
"last_backup_label":"20261001-193612F", ..., "status_code":0, "backup_stale":false
```

A second, separate K8s-specific bug closed the same day: `get_pgbackrest_status`/`run_pgbackrest_backup` used to silently fall through to a bare local command on the *agent's own host* whenever a target resolved to a K8s Pod — not an error, not the real target. Confirmed live with a capturing test runner before the fix (`cmdRunner.Run` called with `name="pgbackrest"` against a K8s-only `infraConfig` entry), fixed by routing K8s-resolved targets through the real `kubectl exec` dispatch shown above instead.

## 2. Part B: WAL archiving — a real operator says no, for a reason worth knowing; a self-managed target says yes

First, against the real CNPG cluster already running in this environment. `db-backup-archiving-broken` and `set_archive_command` are pure SQL, `external_compat: true` — structurally portable to any Postgres reachable over libpq, CNPG included, at least in theory. The first attempt, over CNPG's own unprivileged `app` user, failed exactly as a non-superuser connection should:

```
err="... permission denied for function pg_stat_reset_shared"
err="... permission denied to set parameter \"archive_command\""
```

A plausible, privilege-shaped explanation — so the user enabled real superuser access on their own cluster (`enableSuperuserAccess: true`) and retried with the actual `postgres` role, confirmed via `usesuper=t`. The exact same operation failed again, with a completely different error:

```
could not open file "postgresql.auto.conf": Permission denied
```

Checked directly inside the Pod rather than guess a second time:

```
$ kubectl exec -n db pg-cluster-minkube-1 -- ls -la .../postgresql.auto.conf
-r-------- 1 postgres tape 88 Aug  7 18:37 postgresql.auto.conf

$ kubectl get pod -n db pg-cluster-minkube-1 -o jsonpath='{.spec.containers[0].securityContext}'
{"readOnlyRootFilesystem":true, ...}
```

Mode 400 — read-only even for its own owning OS user — plus a read-only root filesystem. This is CloudNativePG's own deliberate design: Postgres configuration is reconciled from the `Cluster` custom resource itself and the file `ALTER SYSTEM` writes to is locked down specifically so nothing, not even a real superuser, can write to it out-of-band and drift from what the operator expects. No privilege level fixes that. `db-backup-archiving-broken`'s fault injection and `set_archive_command`'s remediation are both architecturally incompatible with CNPG — a real limitation of the capability against any CNPG-managed deployment, not a gap in test coverage. Diagnosis itself is unaffected (`get_backup_status` is pure `SHOW`/`SELECT` and read fine against CNPG's own unprivileged `app` user the whole time).

Against a plain, self-managed `postgres:16` StatefulSet instead (no operator, already running in the cluster for other K8s faults), the identical fault worked as designed — full triage → gate → remediate → recovery-verify cycle, end to end:

```
  Findings          : archive_mode=on; archiving_stale=true; last_failed_wal=000000010000000000000013; ...
  Approve remediation? [y/N]: y
  Approval remediation mode [manual/review/auto] (default: review):
  ...
  Remediation complete:
...
Remediation: RECOVERED in 0.0s (score: 100%)
Diagnostic Result:   [PASS] score=100% (keywords=100% tools=100% judge=100%)
Remediation Result:  [PASS] score=100% (0.0s, playbook)
Overall Result:      [PASS] score=100%
```

Getting there took one real bug fix first. Every earlier attempt against this target failed with `"Purpose \"\" is not in the allowed list [...]"` on the remediation's very first tool call, even though diagnosis had just succeeded moments earlier — a different failure from the CNPG wall above, this one a bug in the test harness itself, not the product. Traced precisely rather than patched blind: the gateway's own `ProceedEscalationRequest` struct has carried a `Purpose` field the whole time (with a documented fallback to the triage run's own recorded purpose) — but the *test harness's* mirror of that struct never had the field at all, across all four of its own "approved" call sites. Confirmed the triage run's own stored purpose was empty too, so the fallback couldn't have saved it either:

```
$ curl ... /api/v1/fleet/playbook-runs/plr_9af61be3 | python3 -c "..."
purpose: None
outcome: transitioned
```

Fixed by adding the missing field and setting it explicitly on every approval path. Two regression tests now assert `purpose == "remediation"` in the actual POST body — then the clean `[PASS]` above, on the very next run.

## 3. Part A: Postgres-backend auditd — a real `helm upgrade`, not `helm template`

A dedicated self-managed Postgres for auditd's own storage, in its own namespace — never sharing a server with a monitored target:

```
$ kubectl create secret generic helpdesk-auditd-dsn -n helpdesk-system \
  --from-literal=dsn='postgres://postgres:***@postgres-auditd.helpdesk-auditd-storage.svc.cluster.local:5432/auditd?sslmode=disable'

$ helm upgrade helpdesk deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values --set governance.auditd.dsnSecret=helpdesk-auditd-dsn
Error: UPGRADE FAILED: Deployment.apps "helpdesk-auditd" is invalid:
  spec.template.spec.containers[0].env[1].valueFrom.secretKeyRef.key: Required value
```

Not a template bug — `governance.auditd.dsnSecretKey` has a real default (`"dsn"`) in the chart's own `values.yaml`. `--reuse-values` reuses a release's *stored* values verbatim; this release predates that key being added to the chart, so it was simply absent from the merge. Fixed by setting both explicitly:

```
$ helm upgrade helpdesk deploy/helm/helpdesk -n helpdesk-system \
  --reuse-values \
  --set governance.auditd.dsnSecret=helpdesk-auditd-dsn \
  --set governance.auditd.dsnSecretKey=dsn
```

The new Pod crash-looped anyway:

```
level=ERROR msg="failed to create govbot store" err="create govbot schema: ERROR: syntax error at or near \"window\" (SQLSTATE 42601)"
```

The exact bug class already fixed in source back when Part A was first built against Docker — `minikube image ls` confirmed why: the cluster's cached `helpdesk:latest` was tagged from `v0.28.0`, built before that fix existed and `imagePullPolicy: IfNotPresent` had never had a reason to refresh it. Rebuilt from current source and reloaded:

```
$ docker build -f helpdesk/Dockerfile -t helpdesk:latest .   # from the repo's parent, sibling to ADK/github/adk-go
$ minikube image load docker.io/library/helpdesk:latest
$ kubectl -n helpdesk-system rollout restart deployment/helpdesk-auditd
deployment "helpdesk-auditd" successfully rolled out
```

Clean startup, real schema, real backend:

```
level=INFO msg="audit service starting" ... db="postgres://postgres:***@postgres-auditd...svc.cluster.local:5432/auditd?sslmode=disable" backend=postgres
level=INFO msg="playbooks: seed complete" seeded=42 skipped=0
```

And a real write, not just startup seeding — triggered through the gateway like any other agent call, then checked directly against Postgres:

```
$ curl -X POST http://localhost:18080/api/v1/query -d '{"agent":"database","message":"call check_connection with connection_string=..."}'
{"tool_calls":["check_connection"], ...}

$ kubectl exec postgres-auditd-0 -n helpdesk-auditd-storage -- psql -U postgres -d auditd \
  -c "SELECT count(*) FROM audit_events;"
 count
-------
     6
```

---

Six real findings, one real platform: a JSON-parsing bug and a dispatch-safety bug in Part C, an architectural wall and a test-harness bug in Part B, a Helm value-merging gotcha and a stale image in Part A. None of them were hypothetical and two were corrected mid-investigation after an initial, plausible-sounding explanation turned out to be wrong — caught by checking the actual stream, the actual file, the actual stored value, before writing the fix. That's the same discipline this project applies everywhere else: verified, not claimed.
