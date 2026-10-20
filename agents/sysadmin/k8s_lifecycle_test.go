package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeK8sCluster is a cmdRunner fake for the Kubernetes lifecycle: one
// StatefulSet (pg-prod-db, matching withK8sInfra's selector) whose pod is
// ready, crash-looping or gone, and the maintenance pods kubectl run starts.
// pgBackRest commands, whether exec'd into the pod or run in a maintenance
// pod, are answered by router.
type fakeK8sCluster struct {
	router      *pgbackrestRouter
	podReady    bool // the instance pod's containers are ready
	replicas    int  // StatefulSet spec.replicas; 0 means no pod
	resumeReady bool // the pod a scale-up creates is ready
	annotations map[string]string
	logsCurrent string // the instance pod's current run's output
	logsPrev    string // its previous run's (crash-looping); "" = none

	calls       [][]string // kubectl args without --context/-n
	maintScript []string   // each maintenance pod's script, in order
	maintPods   map[string]string
	deleted     []string
	execs       int
}

func newFakeK8sCluster(router *pgbackrestRouter, podReady bool) *fakeK8sCluster {
	return &fakeK8sCluster{router: router, podReady: podReady, replicas: 1, annotations: map[string]string{}, maintPods: map[string]string{}}
}

const fakeK8sStatefulSet = `{"metadata":{"name":"pg-prod-db","annotations":%s},"spec":{"replicas":%d,
 "template":{"metadata":{"labels":{"app":"postgres","instance":"prod-db"}},"spec":{"volumes":[{"name":"config","configMap":{"name":"pg-config"}}],
 "containers":[{"name":"postgres","image":"registry.example/pg-pgbackrest:16","volumeMounts":[{"name":"pgdata","mountPath":"/var/lib/postgresql/data"},{"name":"repo","mountPath":"/var/lib/pgbackrest"}]}]}},
 "volumeClaimTemplates":[{"metadata":{"name":"pgdata"}},{"metadata":{"name":"repo"}}]}}`

func (f *fakeK8sCluster) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	if name != "kubectl" {
		return "", fmt.Errorf("unexpected command %s %v", name, args)
	}
	var a []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--context" || args[i] == "-n" {
			i++
			continue
		}
		a = append(a, args[i])
	}
	f.calls = append(f.calls, a)
	j := strings.Join(a, " ")
	switch {
	case containsSeq(a, "get", "pod", "-l") && strings.Contains(j, "jsonpath={.items[0].metadata.name}"):
		if f.replicas == 0 {
			return "", nil
		}
		return "pg-prod-db-0", nil
	case containsSeq(a, "get", "pod", "-l"):
		if f.replicas == 0 {
			return `{"items":[]}`, nil
		}
		ready := "false"
		if f.podReady {
			ready = "true"
		}
		return `{"items":[{"metadata":{"name":"pg-prod-db-0"},"status":{"phase":"Running","containerStatuses":[{"ready":` + ready + `,"restartCount":3}]}}]}`, nil
	case containsSeq(a, "get", "statefulset"):
		ann, _ := json.Marshal(f.annotations)
		return `{"items":[` + fmt.Sprintf(fakeK8sStatefulSet, ann, f.replicas) + `]}`, nil
	case containsSeq(a, "annotate", "statefulset"):
		kv := a[len(a)-1]
		if k, ok := strings.CutSuffix(kv, "-"); ok {
			delete(f.annotations, k)
		} else if k, v, ok := strings.Cut(kv, "="); ok {
			f.annotations[k] = v
		}
		return "statefulset annotated", nil
	case containsSeq(a, "scale", "statefulset"):
		fmt.Sscanf(a[len(a)-1], "--replicas=%d", &f.replicas)
		if f.replicas > 0 {
			f.podReady = f.resumeReady
		}
		return "statefulset scaled", nil
	case containsSeq(a, "exec", "pg-prod-db-0"):
		f.execs++
		if !f.podReady || f.replicas == 0 {
			return "error: unable to upgrade connection: container not found (\"postgres\")", fmt.Errorf("exit status 1")
		}
		for i, s := range a {
			if s == "--" {
				return f.router.Run(ctx, a[i+1], a[i+2:], nil)
			}
		}
	case containsSeq(a, "run"):
		pod := a[1]
		var over struct {
			Spec struct {
				Containers []struct {
					Name    string   `json:"name"`
					Command []string `json:"command"`
				} `json:"containers"`
			} `json:"spec"`
		}
		for _, s := range a {
			if o, ok := strings.CutPrefix(s, "--overrides="); ok {
				if err := json.Unmarshal([]byte(o), &over); err != nil {
					return "", err
				}
			}
		}
		if len(over.Spec.Containers) != 1 || over.Spec.Containers[0].Name != pod || len(over.Spec.Containers[0].Command) != 4 {
			return "", fmt.Errorf("maintenance pod overrides: %+v", over)
		}
		script := over.Spec.Containers[0].Command[3]
		f.maintPods[pod] = script
		f.maintScript = append(f.maintScript, script)
		return "pod/" + pod + " created", nil
	case containsSeq(a, "get", "pod") && strings.Contains(j, "jsonpath={.status.phase}"):
		if _, err := f.router.Run(ctx, "su", []string{"postgres", "-c", f.maintPods[a[2]]}, nil); err != nil {
			return "Failed", nil
		}
		return "Succeeded", nil
	case containsSeq(a, "get", "pod") && strings.Contains(j, "exitCode"):
		return "1", nil
	case containsSeq(a, "logs"):
		if script, ok := f.maintPods[a[1]]; ok {
			out, _ := f.router.Run(ctx, "su", []string{"postgres", "-c", script}, nil)
			return out, nil
		}
		if strings.Contains(j, "--previous") {
			if f.logsPrev == "" {
				return "previous terminated container not found", fmt.Errorf("exit status 1")
			}
			return f.logsPrev, nil
		}
		return f.logsCurrent, nil
	case containsSeq(a, "delete", "pod"):
		f.deleted = append(f.deleted, a[2])
		return "pod deleted", nil
	}
	return "", fmt.Errorf("unexpected kubectl %s", j)
}

func (f *fakeK8sCluster) callsContaining(sub ...string) int {
	n := 0
	for _, c := range f.calls {
		if containsSeq(c, sub...) {
			n++
		}
	}
	return n
}

func withFakeK8sCluster(t *testing.T, f *fakeK8sCluster) {
	t.Helper()
	withK8sInfra(t)
	old := cmdRunner
	cmdRunner = f
	t.Cleanup(func() { cmdRunner = old })
	oldPoll := k8sPollInterval
	k8sPollInterval = 0
	t.Cleanup(func() { k8sPollInterval = oldPoll })
}

// A ready pod: pgBackRest runs in it with kubectl exec, as before v0.32.
func TestGetPgBackRestStatus_K8s_ReadyPodExec(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{info: realPgBackRestInfoJSON}, true)
	withFakeK8sCluster(t, f)
	res, err := getPgBackRestStatusTool(mockToolContext{context.Background()}, GetPgBackRestStatusArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("get_pgbackrest_status: %v", err)
	}
	if res.Stanza != "main" || res.WALContinuity == nil || res.WALContinuity.Status != WALContinuous {
		t.Fatalf("result = stanza %q, wal %+v; want main, continuous", res.Stanza, res.WALContinuity)
	}
	if f.execs == 0 || len(f.maintScript) != 0 {
		t.Errorf("execs %d, maintenance pods %d: want every read exec'd into the ready pod", f.execs, len(f.maintScript))
	}
}

// A crash-looping pod can't be exec'd into: the same reads run in a
// maintenance pod with its volumes, and the WAL check still answers.
func TestGetPgBackRestStatus_K8s_CrashLoopingPodUsesMaintenancePod(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{info: realPgBackRestInfoJSON}, false)
	withFakeK8sCluster(t, f)
	res, err := getPgBackRestStatusTool(mockToolContext{context.Background()}, GetPgBackRestStatusArgs{Target: "prod_db"})
	if err != nil {
		t.Fatalf("get_pgbackrest_status: %v", err)
	}
	if res.WALContinuity == nil || res.WALContinuity.Status != WALContinuous {
		t.Fatalf("WALContinuity = %+v, want continuous through maintenance pods", res.WALContinuity)
	}
	if f.execs != 0 || len(f.maintScript) < 2 || !strings.Contains(f.maintScript[0], "'info'") {
		t.Errorf("execs %d, maintenance scripts %q: want no exec, info and the WAL reads in maintenance pods", f.execs, f.maintScript)
	}
	if len(f.deleted) != len(f.maintScript) {
		t.Errorf("deleted %v for %d maintenance pods: each must be deleted", f.deleted, len(f.maintScript))
	}
}

// The restore on a crash-looping pod: not serving, WAL checked in maintenance
// pods, StatefulSet scaled to 0 with its replica count recorded, then the
// restore script in a maintenance pod with the saved pg_wal copy on the repo
// volume, and the instance left scaled down for restart_container.
func TestRestoreFromBackup_K8s_QuiesceThenMaintenancePod(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{info: realPgBackRestInfoJSON,
		restoreOut: "restore command end: completed successfully (200ms)\n"}, false)
	withFakeK8sCluster(t, f)
	res, err := restoreFromBackupTool(mockToolContext{context.Background()}, RestoreFromBackupArgs{Target: "prod_db", Stanza: "main"})
	if err != nil {
		t.Fatalf("restore_from_backup: %v", err)
	}
	if !strings.Contains(res.Output, "completed successfully") {
		t.Errorf("Output = %q", res.Output)
	}
	if f.execs != 0 {
		t.Errorf("exec'd into the crash-looping pod %d times", f.execs)
	}
	if f.replicas != 0 || f.annotations[k8sResumeReplicasAnnotation] != "1" {
		t.Errorf("replicas %d, annotations %v: want scaled to 0 with 1 recorded", f.replicas, f.annotations)
	}
	last := f.maintScript[len(f.maintScript)-1]
	if !strings.HasPrefix(last, "HELPDESK_WAL_SAVE_IN_REPO=1; export HELPDESK_WAL_SAVE_IN_REPO\n") || !strings.Contains(last, "--delta restore") {
		t.Errorf("restore maintenance script = %q, want the restore with the pg_wal copy saved in the repo", last)
	}
	// Scaled down only after the WAL check: the restore is the first run after.
	scaleAt, restoreAt := -1, -1
	for i, c := range f.calls {
		if containsSeq(c, "scale", "statefulset") && scaleAt < 0 {
			scaleAt = i
		}
		if containsSeq(c, "run") && strings.Contains(f.maintPods[c[1]], "--delta restore") {
			restoreAt = i
		}
	}
	if scaleAt < 0 || restoreAt < scaleAt {
		t.Errorf("scale at call %d, restore at %d: the StatefulSet must be scaled down before the restore", scaleAt, restoreAt)
	}
	if !strings.Contains(res.Quiesced, "scaled from 1 to 0") || !strings.Contains(res.Quiesced, "restart_container") {
		t.Errorf("Quiesced = %q, want it to say scaled to 0 and to resume with restart_container", res.Quiesced)
	}
}

// A pod that is up and serving is never restored over, and nothing is
// scaled down.
func TestRestoreFromBackup_K8s_RefusesWhenServing(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{info: realPgBackRestInfoJSON, readyOut: "accepting connections"}, true)
	withFakeK8sCluster(t, f)
	_, err := restoreFromBackupImpl(context.Background(), RestoreFromBackupArgs{Target: "prod_db", Stanza: "main"})
	if err == nil || !strings.Contains(err.Error(), "still accepting connections") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if f.callsContaining("scale") != 0 || len(f.maintScript) != 0 {
		t.Errorf("scaled %d times, %d maintenance pods: a refused restore must change nothing", f.callsContaining("scale"), len(f.maintScript))
	}
}

// restart_container resumes what restore_from_backup quiesced: back to the
// recorded replicas, record cleared.
func TestRestartContainer_K8s_ResumesRecordedReplicas(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{}, false)
	f.replicas, f.resumeReady = 0, true
	f.annotations[k8sResumeReplicasAnnotation] = "1"
	withFakeK8sCluster(t, f)
	res, err := restartContainerImpl(context.Background(), RestartContainerArgs{Target: "prod_db", Reason: "resume after restore"})
	if err != nil || !res.Success || res.Runtime != "kubectl" {
		t.Fatalf("restart_container = %+v, %v", res, err)
	}
	if f.replicas != 1 || f.annotations[k8sResumeReplicasAnnotation] != "" {
		t.Errorf("replicas %d, annotations %v: want back to 1, record cleared", f.replicas, f.annotations)
	}
}

// On a StatefulSet nobody scaled down, restart_container deletes the pod
// (recreated on the same volumes), the Kubernetes form of a restart.
func TestRestartContainer_K8s_DeletesPodWhenNotQuiesced(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{}, true)
	withFakeK8sCluster(t, f)
	res, err := restartContainerImpl(context.Background(), RestartContainerArgs{Target: "prod_db"})
	if err != nil || !res.Success || len(f.deleted) != 1 || f.deleted[0] != "pg-prod-db-0" || f.callsContaining("scale") != 0 {
		t.Fatalf("restart_container = %+v, %v; deleted %v, scaled %d", res, err, f.deleted, f.callsContaining("scale"))
	}
}

// A crash-looping container's fatal error is in its previous run's output:
// get_host_logs returns that first, then the current run's.
func TestGetHostLogs_K8s_IncludesPreviousRun(t *testing.T) {
	f := newFakeK8sCluster(&pgbackrestRouter{}, false)
	f.logsPrev = "FATAL:  could not find the database system\n"
	f.logsCurrent = "starting PostgreSQL\n"
	withFakeK8sCluster(t, f)
	res, err := getHostLogsImpl(context.Background(), GetHostLogsArgs{Target: "prod_db", Lines: 50})
	if err != nil {
		t.Fatalf("get_host_logs: %v", err)
	}
	if res.Runtime != "kubectl" || !strings.Contains(res.Logs, "could not find the database system") ||
		strings.Index(res.Logs, "could not find") > strings.Index(res.Logs, "starting PostgreSQL") {
		t.Errorf("logs = %q, want the previous run's FATAL before the current run", res.Logs)
	}
	if !containsSeq(f.calls[len(f.calls)-1], "--tail=50") {
		t.Errorf("last call %v, want --tail=50", f.calls[len(f.calls)-1])
	}
}

// The maintenance pod is the StatefulSet's image with its postgres
// container's mounts, its own volumes and ordinal 0's claims, the container
// named after the pod.
func TestK8sMaintenancePodOverrides(t *testing.T) {
	var sts k8sStatefulSetJSON
	if err := json.Unmarshal([]byte(fmt.Sprintf(fakeK8sStatefulSet, "{}", 1)), &sts); err != nil {
		t.Fatal(err)
	}
	over, image, err := k8sMaintenancePodOverrides(sts, "pg-prod-db-helpdesk-maint-x", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if image != "registry.example/pg-pgbackrest:16" {
		t.Errorf("image = %q", image)
	}
	for _, want := range []string{
		`"claimName":"pgdata-pg-prod-db-0"`, `"claimName":"repo-pg-prod-db-0"`, `"configMap":{"name":"pg-config"}`,
		`"mountPath":"/var/lib/postgresql/data"`, `"name":"pg-prod-db-helpdesk-maint-x"`,
		`"command":["su","postgres","-c","echo hi"]`, `"restartPolicy":"Never"`,
	} {
		if !strings.Contains(over, want) {
			t.Errorf("overrides missing %s:\n%s", want, over)
		}
	}
}

func TestSelectorMatches(t *testing.T) {
	labels := map[string]string{"app": "postgres", "instance": "prod-db", "tier": "db"}
	for sel, want := range map[string]bool{
		"app=postgres": true, "app=postgres,instance=prod-db": true, "app = postgres": true, "app==postgres": true,
		"app=mysql": false, "app=postgres,instance=other": false, "": false, "app": false,
	} {
		if got := selectorMatches(sel, labels); got != want {
			t.Errorf("selectorMatches(%q) = %v, want %v", sel, got, want)
		}
	}
}

// The Docker/Podman maintenance run, built but not yet the default: the
// container's own image and volumes, script as postgres.
func TestContainerMaintenanceArgs(t *testing.T) {
	got := strings.Join(containerMaintenanceArgs("pg", "pg-image:16", "pgbackrest info"), " ")
	if got != "run --rm --volumes-from pg --entrypoint su pg-image:16 postgres -c pgbackrest info" {
		t.Errorf("args = %q", got)
	}
}
