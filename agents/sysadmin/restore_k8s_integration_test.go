//go:build integration

// TestIntegration_K8s_RestoreRefusesOnRealWALGap is
// TestIntegration_RestoreFromBackup_RefusesOnRealWALGap against the
// self-managed pgBackRest pod on a real Kubernetes cluster, through the
// sysadmin tools' kubectl exec dispatch: the latest backup's start segment is
// moved aside in the pod's repo, get_pgbackrest_status must report the chain
// broken, and after a real crash restore_from_backup must refuse without
// touching PGDATA. There's no agent restore chain on Kubernetes yet (v0.33),
// so this is the tool-level check of item 1's refusal on K8s.
//
// Opt-in, since it crashes and restores the pod: set
// HELPDESK_K8S_PGBACKREST_CONTEXT to the kubeconfig context (e.g. minikube),
// in its own namespace, which must end in -destruct (default db-destruct;
// HELPDESK_K8S_PGBACKREST_NAMESPACE overrides). `make integration-k8s` creates
// the namespace and the pod, takes a fresh backup and runs these tests.
package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"helpdesk/internal/infra"
)

const k8sPgBackRestPodSelector = "app=pgbackrest-demo"

type k8sPgBackRestFixture struct {
	t        *testing.T
	ctxName  string
	ns       string
	pod      string
	kubeArgs []string
}

func k8sPgBackRest(t *testing.T) *k8sPgBackRestFixture {
	t.Helper()
	ctxName := os.Getenv("HELPDESK_K8S_PGBACKREST_CONTEXT")
	if ctxName == "" {
		t.Skip("HELPDESK_K8S_PGBACKREST_CONTEXT not set — opt-in: this test crashes and restores the K8s pgBackRest pod")
	}
	ns := os.Getenv("HELPDESK_K8S_PGBACKREST_NAMESPACE")
	if ns == "" {
		ns = "db-destruct"
	}
	// Same guard as make integration-k8s: these tests crash and restore the
	// namespace's database, so never one a real or test database lives in.
	if !strings.HasSuffix(ns, "-destruct") {
		t.Fatalf("HELPDESK_K8S_PGBACKREST_NAMESPACE=%q must end in -destruct: these tests crash and restore its database", ns)
	}
	f := &k8sPgBackRestFixture{t: t, ctxName: ctxName, ns: ns, kubeArgs: []string{"--context", ctxName, "-n", ns}}
	f.pod = strings.TrimSpace(f.kubectl("get", "pod", "-l", k8sPgBackRestPodSelector, "-o", "jsonpath={.items[0].metadata.name}"))
	if f.pod == "" {
		t.Fatalf("no %s pod in namespace %s", k8sPgBackRestPodSelector, ns)
	}
	old := infraConfig
	infraConfig = &infra.Config{
		DBServers: map[string]infra.DBServer{
			"pgbackrest-k8s": {
				Name:             "pgBackRest K8s Demo",
				ConnectionString: "host=pgbackrest-demo." + ns + ".svc.cluster.local port=5432 dbname=testdb user=postgres password=testpass",
				K8sCluster:       "local",
				K8sNamespace:     ns,
				K8sPodSelector:   k8sPgBackRestPodSelector,
			},
		},
		K8sClusters: map[string]infra.K8sCluster{"local": {Name: "local", Context: ctxName}},
	}
	t.Cleanup(func() { infraConfig = old })
	return f
}

// kubectl runs kubectl in the fixture's context and namespace and fails the
// test on error.
func (f *k8sPgBackRestFixture) kubectl(args ...string) string {
	f.t.Helper()
	out, err := exec.Command("kubectl", append(append([]string{}, f.kubeArgs...), args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("kubectl %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// exec runs a shell script in the pod (as root; su postgres inside where
// needed), returning output and error without failing the test.
func (f *k8sPgBackRestFixture) exec(script string) (string, error) {
	out, err := exec.Command("kubectl", append(append([]string{}, f.kubeArgs...), "exec", f.pod, "--", "sh", "-c", script)...).CombinedOutput()
	return string(out), err
}

// restartPod deletes the pod; the StatefulSet recreates it on the same
// volumes and the image's entrypoint keeps the container up even when
// postgres can't start. Waits for the container to have started.
func (f *k8sPgBackRestFixture) restartPod() {
	f.t.Helper()
	f.kubectl("delete", "pod", f.pod, "--wait=true")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("kubectl", append(append([]string{}, f.kubeArgs...), "get", "pod", f.pod,
			"-o", "jsonpath={.status.containerStatuses[0].started}")...).Output()
		if strings.TrimSpace(string(out)) == "true" {
			time.Sleep(5 * time.Second) // postgres's own startup (or crash)
			return
		}
		time.Sleep(2 * time.Second)
	}
	f.t.Fatalf("pod %s did not start within 3m after delete", f.pod)
}

// repair restores the pod's PGDATA from the latest backup if postgres is
// down, then restarts the pod and waits for it to be Ready. Same guard as
// restoreIntegrationTeardown: never restore over a running server.
func (f *k8sPgBackRestFixture) repair() {
	f.t.Helper()
	if _, err := f.exec("pg_isready -q"); err == nil {
		return
	}
	if out, err := f.exec(`su postgres -c 'PGDATA=$(grep "^pg1-path=" /etc/pgbackrest.conf | cut -d= -f2); rm -f "$PGDATA/postmaster.pid"; pgbackrest --stanza=main --delta restore'`); err != nil {
		f.t.Errorf("repair restore: %v: %s", err, out)
	}
	f.restartPod()
	if out, err := exec.Command("kubectl", append(append([]string{}, f.kubeArgs...), "wait", "--for=condition=Ready", "pod/"+f.pod, "--timeout=180s")...).CombinedOutput(); err != nil {
		f.t.Errorf("pod not Ready after repair: %v: %s", err, out)
	}
}

func TestIntegration_K8s_RestoreRefusesOnRealWALGap(t *testing.T) {
	f := k8sPgBackRest(t)
	ctx := context.Background()
	t.Cleanup(f.repair) // registered first, runs last: after the segment is back

	before, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-k8s"})
	if err != nil {
		t.Fatalf("precondition: get_pgbackrest_status over kubectl exec: %v", err)
	}
	if before.WALContinuity == nil || before.WALContinuity.Status != WALContinuous {
		t.Fatalf("precondition: WAL continuity = %+v, want continuous on the healthy pod", before.WALContinuity)
	}
	seg := before.WALContinuity.BackupStartWAL
	archiveDir := "/var/lib/pgbackrest/archive/main/" + before.WALContinuity.ArchiveID + "/" + seg[:16]

	if out, err := f.exec(`set -e; g=$(ls ` + archiveDir + `/` + seg + `-*); mv "$g" "$g.hidden-by-test"`); err != nil {
		t.Fatalf("hiding WAL segment %s: %v: %s", seg, err, out)
	}
	t.Cleanup(func() {
		if out, err := f.exec(`for g in ` + archiveDir + `/*.hidden-by-test; do [ -e "$g" ] && mv "$g" "${g%.hidden-by-test}"; done; true`); err != nil {
			t.Errorf("cleanup: restoring WAL segment %s: %v: %s", seg, err, out)
		}
	})

	after, err := getPgBackRestStatusImpl(ctx, GetPgBackRestStatusArgs{Target: "pgbackrest-k8s"})
	if err != nil {
		t.Fatalf("get_pgbackrest_status with the segment hidden: %v", err)
	}
	if wal := after.WALContinuity; wal == nil || wal.Status != WALBroken || wal.ConsistencyReachable ||
		len(wal.Gaps) == 0 || wal.Gaps[0].FirstMissing != seg {
		t.Fatalf("WAL continuity = %+v, want broken at %s with consistency unreachable", after.WALContinuity, seg)
	}

	// A real crash: pg_control gone, then the pod restarted so postgres
	// attempts startup and fails (a running backend doesn't read it again).
	if out, err := f.exec("rm -f /var/lib/postgresql/data/global/pg_control && touch /var/lib/postgresql/data/walcheck-canary"); err != nil {
		t.Fatalf("corrupting pg_control: %v: %s", err, out)
	}
	f.restartPod()
	if _, err := f.exec("pg_isready -q"); err == nil {
		t.Fatal("postgres is still up after pg_control was removed and the pod restarted")
	}

	_, err = restoreFromBackupImpl(ctx, RestoreFromBackupArgs{Target: "pgbackrest-k8s", Stanza: "main"})
	if err == nil {
		t.Fatal("restore_from_backup succeeded with the backup's own WAL missing, want a refusal")
	}
	if !strings.Contains(err.Error(), "WAL continuity is broken") {
		t.Errorf("error = %v, want a WAL-continuity refusal", err)
	}
	if _, err := f.exec("test -f /var/lib/postgresql/data/walcheck-canary"); err != nil {
		t.Errorf("canary file is gone from PGDATA: the refused restore touched the data directory")
	}
}
