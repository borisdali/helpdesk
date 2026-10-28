package playbooks_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"helpdesk/playbooks"
)

// The point-in-time recovery pair (v0.33): a read-only triage that plans a
// target a human supplied, and a remediation that restores to it. The safety
// of the pair is in the code (the gateway's gate, the tool's approval check),
// not in this prose; these tests pin the parts of the prose that the code
// relies on: the model is never told to pass a target, the restore playbook
// is terminal, and the triage can only hand on to the restore.

type pitrPB struct {
	SeriesID      string   `yaml:"series_id"`
	PlaybookType  string   `yaml:"playbook_type"`
	EntryPoint    bool     `yaml:"entry_point"`
	ExecutionMode string   `yaml:"execution_mode"`
	AgentName     string   `yaml:"agent_name"`
	ApprovalMode  string   `yaml:"approval_mode"`
	TransitionsTo []string `yaml:"transitions_to"`
	Guidance      string   `yaml:"guidance"`
}

func loadPITR(t *testing.T, file string) pitrPB {
	t.Helper()
	b, err := playbooks.FS.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var pb pitrPB
	if err := yaml.Unmarshal(b, &pb); err != nil {
		t.Fatal(err)
	}
	return pb
}

// flat collapses the YAML's line wrapping, so a phrase is matched wherever the
// text happens to break.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// signalLines are the lines that start with a routing signal.
func signalLines(guidance string) []string {
	var out []string
	for _, l := range strings.Split(guidance, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "TRANSITION_TO:") || strings.HasPrefix(l, "ESCALATE_TO:") {
			out = append(out, l)
		}
	}
	return out
}

func TestPITRPlaybooks_Triage(t *testing.T) {
	pb := loadPITR(t, "pgbackrest-pitr-triage.yaml")
	if pb.SeriesID != "pbs_pgbackrest_pitr_triage" || pb.PlaybookType != "triage" || !pb.EntryPoint || pb.ExecutionMode != "agent" || pb.AgentName != "sysadmin_agent" {
		t.Errorf("triage = %+v", pb)
	}
	if len(pb.TransitionsTo) != 1 || pb.TransitionsTo[0] != "pbs_pgbackrest_pitr_remediate" {
		t.Errorf("transitions_to = %v, want only the restore", pb.TransitionsTo)
	}
	for _, l := range signalLines(pb.Guidance) {
		if l != "TRANSITION_TO: pbs_pgbackrest_pitr_remediate" && l != "ESCALATE_TO: none" {
			t.Errorf("unexpected routing line %q", l)
		}
	}
	for _, term := range []string{
		"plan_point_in_time_recovery with the connection_string (target) only",
		"ignores any target you pass",
		"Never choose, adjust, round or \"improve\" a target",
		"plan_digest",
		"Do NOT call restore_to_point_in_time or restore_from_backup",
		"a person reviews this plan at the gate",
		"Stopping a serving instance is not",
	} {
		if !strings.Contains(flat(pb.Guidance), term) {
			t.Errorf("triage guidance lacks %q", term)
		}
	}
	// Read-only: the only write/destructive tool names appear in prohibitions.
	for _, tool := range []string{"restore_to_point_in_time", "restore_from_backup", "restart_container", "restart_service", "run_pgbackrest_backup"} {
		for _, sentence := range strings.Split(flat(pb.Guidance), ". ") {
			if strings.Contains(sentence, tool) && !strings.Contains(sentence, "Do NOT") && !strings.Contains(sentence, "restore tool") {
				t.Errorf("triage mentions %s outside a prohibition: %q", tool, sentence)
			}
		}
	}
}

func TestPITRPlaybooks_Remediate(t *testing.T) {
	pb := loadPITR(t, "pgbackrest-pitr-remediate.yaml")
	if pb.SeriesID != "pbs_pgbackrest_pitr_remediate" || pb.PlaybookType != "remediation" || pb.EntryPoint || pb.ExecutionMode != "agent" || pb.AgentName != "sysadmin_agent" || pb.ApprovalMode != "manual" {
		t.Errorf("remediate = %+v", pb)
	}
	if len(pb.TransitionsTo) != 0 {
		t.Errorf("a terminal action playbook lists transitions_to %v", pb.TransitionsTo)
	}
	if lines := signalLines(pb.Guidance); len(lines) != 0 {
		t.Errorf("a terminal playbook emits routing lines: %v", lines)
	}
	for _, term := range []string{
		"restore_to_point_in_time with the connection_string (target) only",
		"You never choose, restate or adjust",
		"the digest no longer matches",
		"target_not_reached",
		"Do not restart it and do not retry the restore",
		"Do NOT retry restore_to_point_in_time",
		"Do NOT call restore_from_backup",
		"runtime=kubectl → restart_container",
		"Everything committed after <normalized_target>",
		"new timeline",
	} {
		if !strings.Contains(flat(pb.Guidance), term) {
			t.Errorf("remediate guidance lacks %q", term)
		}
	}
}

// Neither playbook gives the model a place to put a target: the tools take it
// from the approval, and the model-facing restore tool has no such argument.
// (The triage does tell the model to REPORT the plan_digest in FINDINGS; that
// is output a person reads, not a value passed into a tool.)
func TestPITRPlaybooks_NeverTellTheModelToPassATarget(t *testing.T) {
	for _, file := range []string{"pgbackrest-pitr-triage.yaml", "pgbackrest-pitr-remediate.yaml"} {
		pb := loadPITR(t, file)
		for _, banned := range []string{"recovery_target_type", "recovery_target_value", "--target=", "--type=time", "acknowledge"} {
			if strings.Contains(pb.Guidance, banned) {
				t.Errorf("%s: guidance contains %q — the model must not be handed a way to supply the target", file, banned)
			}
		}
	}
}
