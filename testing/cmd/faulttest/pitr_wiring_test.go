package main

import (
	"strings"
	"testing"

	"helpdesk/testing/faultlib"
)

// The CLI's wrappers pass a PITR fault's recovery target through: the injector
// keeps its output (it rebuilds a faultlib.Injector per call), and the
// Remediator hands it to the one that approves the gate.
func TestCLIInjector_KeepsTheInjectOutput(t *testing.T) {
	inj := NewInjector(&HarnessConfig{})
	f := Failure{Inject: faultlib.InjectSpec{Type: "shell_exec", ScriptInline: "echo 'PITR_TARGET time=2026-10-28T10:00:00Z lsn=0/1'"}}
	if err := inj.Inject(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inj.InjectOutput(), "PITR_TARGET time=2026-10-28T10:00:00Z") {
		t.Errorf("InjectOutput = %q", inj.InjectOutput())
	}
	var _ faultlib.InjectOutputter = inj // the cycle looks for exactly this
}

func TestCLIRemediator_SetPITR(t *testing.T) {
	r := NewRemediator(&HarnessConfig{})
	f := Failure{ID: "db-pitr-dropped-table", PITR: &faultlib.PITRSpec{}}
	if err := r.SetPITR(f, "no marker here"); err == nil {
		t.Error("a PITR fault with no target must be an error")
	}
	if err := r.SetPITR(f, "PITR_TARGET time=2026-10-28T10:00:00Z"); err != nil {
		t.Errorf("SetPITR: %v", err)
	}
	if err := r.SetPITR(Failure{ID: "db-other"}, ""); err != nil {
		t.Errorf("a fault that is not a PITR one: %v", err)
	}
}
