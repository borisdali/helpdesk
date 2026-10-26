package playbooks_test

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"helpdesk/playbooks"
)

// The agent-driven restore chain on Kubernetes (v0.33): the entry hop hands a
// Kubernetes data-loss case to the host triage, which reads the pod's logs
// instead of passing it straight to the pod-crash triage, and the restore
// playbook knows to resume the StatefulSet with restart_container. Each of
// these was missing in v0.32 (the tools worked in-cluster; the routing did
// not), so this pins the guidance that routes it.

func shippedPlaybook(t *testing.T, file string) (version, guidance string) {
	t.Helper()
	b, err := playbooks.FS.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	var pb struct {
		Version  string `yaml:"version"`
		Guidance string `yaml:"guidance"`
	}
	if err := yaml.Unmarshal(b, &pb); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	return pb.Version, pb.Guidance
}

func versionAtLeast(got, want string) bool {
	var gMaj, gMin, wMaj, wMin int
	if _, err := fmt.Sscanf(got, "%d.%d", &gMaj, &gMin); err != nil {
		return false
	}
	_, _ = fmt.Sscanf(want, "%d.%d", &wMaj, &wMin)
	return gMaj > wMaj || (gMaj == wMaj && gMin >= wMin)
}

func TestKubernetesChain_Guidance(t *testing.T) {
	for _, c := range []struct {
		file, minVersion string
		terms            []string
		forbidden        []string
	}{
		{
			// Hop 1 must not tell the DB agent to use a tool it does not have.
			file: "database-data-loss-triage.yaml", minVersion: "1.10",
			terms:     []string{`hosting of "Kubernetes: ..."`, "get_pod_logs belongs to the Kubernetes agent", "ESCALATE_TO: pbs_sysadmin_host_triage"},
			forbidden: []string{"use get_pod_logs to identify the specific error"},
		},
		{
			// Hop 2: a Kubernetes pod still gets the data-loss test, from kubectl logs.
			file: "sysadmin-host-triage.yaml", minVersion: "2.4",
			terms: []string{
				"runtime=kubectl",
				"call get_host_logs with lines=300",
				"kubectl logs",
				"Do NOT call read_pg_log_file",
				"data_loss_restore_candidate=true",
				"TRANSITION_TO: pbs_pgbackrest_health_triage",
				"ESCALATE_TO: pbs_k8s_pod_crash_triage", // the fallback when the signature is absent
			},
		},
		{
			// Hop 4: resumes the StatefulSet, no restart_service / docker.
			file: "pgbackrest-restore-remediate.yaml", minVersion: "1.10",
			terms: []string{"runtime=kubectl → call restart_container", "scales the StatefulSet back up", "On Kubernetes get_host_logs reads `kubectl logs`", "expect this first check_host to FAIL once restore_from_backup has run", "Do not retry check_host until after restart_container",
				// found 2026-10-26 on the Docker fixture: a running container is not a running PostgreSQL
				"Do not refuse on your own account because check_host reports a container as running",
				"a Docker or Podman container can stay up after the PostgreSQL inside it has died",
				"let restore_from_backup decide"},
		},
	} {
		t.Run(c.file, func(t *testing.T) {
			version, guidance := shippedPlaybook(t, c.file)
			guidance = strings.Join(strings.Fields(guidance), " ") // line breaks and indentation are not content
			if !versionAtLeast(version, c.minVersion) {
				t.Errorf("version = %q, want %s or later (the first with the Kubernetes routing)", version, c.minVersion)
			}
			for _, term := range c.terms {
				if !strings.Contains(guidance, term) {
					t.Errorf("guidance never mentions %q", term)
				}
			}
			for _, term := range c.forbidden {
				if strings.Contains(guidance, term) {
					t.Errorf("guidance still says %q", term)
				}
			}
		})
	}
}
