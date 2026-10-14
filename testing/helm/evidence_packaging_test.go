package helm_test

// Packaging guard for objective-evidence rule files. Every agent that ships an
// agents/<agent>/objective_evidence.yaml loads it at startup from an env var
// (HELPDESK_<AGENT>_EVIDENCE_RULES); if a deployment path never sets that var,
// the agent still starts, logs a WARN, and silently runs with its
// objective-evidence force-gate disabled. That happened for the sysadmin agent
// in v0.31 — its rules (the pgbackrest_backup_unhealthy gate the restore chain
// relies on) were wired into no packaging path at all — and the chart's own
// copy of the database rules had drifted from the source, missing
// backup_archiving_stale. These tests are pure file checks (no helm binary
// needed) so they run under plain `go test ./...` too.

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// evidenceAgents returns the agent directory names that ship an
// objective_evidence.yaml, e.g. ["database", "k8s", "sysadmin"].
func evidenceAgents(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repoRoot(t), "agents", "*", "objective_evidence.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no agents/*/objective_evidence.yaml found — glob or repo layout changed")
	}
	var agents []string
	for _, m := range matches {
		agents = append(agents, filepath.Base(filepath.Dir(m)))
	}
	return agents
}

// envVarFor maps an agent directory to the env var its main.go reads.
func envVarFor(agent string) string {
	if agent == "database" {
		return "HELPDESK_DB_EVIDENCE_RULES"
	}
	return "HELPDESK_" + strings.ToUpper(agent) + "_EVIDENCE_RULES"
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestEvidenceRules_AgentReadsExpectedEnvVar pins the env-var naming the rest
// of this file assumes, so a rename in an agent's main.go can't make the
// packaging checks below pass against a var nothing reads.
func TestEvidenceRules_AgentReadsExpectedEnvVar(t *testing.T) {
	for _, agent := range evidenceAgents(t) {
		src := readRepoFile(t, filepath.Join("agents", agent, "main.go"))
		if !strings.Contains(src, `"`+envVarFor(agent)+`"`) {
			t.Errorf("agents/%s/main.go does not read %s", agent, envVarFor(agent))
		}
	}
}

// TestEvidenceRules_WiredIntoEveryPackagingPath fails when an agent's rules
// file exists but some deployment path never ships it or never points the
// agent at it.
func TestEvidenceRules_WiredIntoEveryPackagingPath(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	startall := readRepoFile(t, "deploy/host/startall.sh")
	hostEnv := readRepoFile(t, "deploy/host/.env.example")
	compose := readRepoFile(t, "deploy/docker-compose/docker-compose.yaml")
	composeEnv := readRepoFile(t, "deploy/docker-compose/.env.example")
	configMap := readRepoFile(t, "deploy/helm/helpdesk/templates/configmap.yaml")
	values := readRepoFile(t, "deploy/helm/helpdesk/values.yaml")

	for _, agent := range evidenceAgents(t) {
		t.Run(agent, func(t *testing.T) {
			src := "agents/" + agent + "/objective_evidence.yaml"
			bundled := "objective_evidence-" + agent + ".yaml"
			envVar := envVarFor(agent)

			// Makefile: binaries tarball + deploy bundle (docker-compose and
			// host dirs) each copy the file, and the bundle's compose sed
			// rewrites the repo-relative mount path to the bundled name.
			if got := strings.Count(makefile, "cp "+src+" "); got < 3 {
				t.Errorf("Makefile copies %s %d time(s), want 3 (binaries tarball, bundle docker-compose/, bundle host/)", src, got)
			}
			if !strings.Contains(makefile, "s|../../"+src+"|./"+bundled+"|") {
				t.Errorf("Makefile bundle target does not rewrite the compose mount path for %s", src)
			}

			// Host deploy.
			if !strings.Contains(startall, envVar+`="${`+envVar+`:-$SCRIPT_DIR/`+bundled+`}"`) {
				t.Errorf("deploy/host/startall.sh does not pass %s with the bundled default", envVar)
			}
			if !strings.Contains(hostEnv, envVar+"=./"+bundled) {
				t.Errorf("deploy/host/.env.example does not set %s", envVar)
			}

			// Docker Compose.
			if !strings.Contains(compose, envVar+": /etc/helpdesk/objective_evidence.yaml") {
				t.Errorf("deploy/docker-compose/docker-compose.yaml does not set %s", envVar)
			}
			if !strings.Contains(compose, "${"+envVar+"_HOST:-../../"+src+"}:/etc/helpdesk/objective_evidence.yaml:ro") {
				t.Errorf("deploy/docker-compose/docker-compose.yaml does not mount %s", src)
			}
			if !strings.Contains(composeEnv, envVar+"_HOST") {
				t.Errorf("deploy/docker-compose/.env.example does not document %s_HOST", envVar)
			}

			// Helm.
			chartFile := "objective-evidence-" + agent + ".yaml"
			if !strings.Contains(configMap, `"files/`+chartFile+`"`) {
				t.Errorf("Helm configmap.yaml does not embed files/%s", chartFile)
			}
			if !strings.Contains(values, "files/"+chartFile) {
				t.Errorf("Helm values.yaml does not declare objectiveEvidence for %s", agent)
			}
		})
	}
}

// TestEvidenceRules_HelmChartCopyMatchesSource fails when the chart's bundled
// copy (Helm can only .Files.Get from inside the chart directory) drifts from
// the agent's source of truth. Fix by re-copying:
//
//	cp agents/<agent>/objective_evidence.yaml deploy/helm/helpdesk/files/objective-evidence-<agent>.yaml
func TestEvidenceRules_HelmChartCopyMatchesSource(t *testing.T) {
	root := repoRoot(t)
	for _, agent := range evidenceAgents(t) {
		t.Run(agent, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, "agents", agent, "objective_evidence.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			chartRel := filepath.Join("deploy", "helm", "helpdesk", "files", "objective-evidence-"+agent+".yaml")
			chart, err := os.ReadFile(filepath.Join(root, chartRel))
			if err != nil {
				t.Fatalf("%s missing: %v", chartRel, err)
			}
			if !bytes.Equal(src, chart) {
				t.Errorf("%s has drifted from agents/%s/objective_evidence.yaml — re-copy it", chartRel, agent)
			}
		})
	}
}
