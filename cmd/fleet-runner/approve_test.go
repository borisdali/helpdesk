package main

import (
	"testing"

	"helpdesk/internal/audit"
)

func TestJobActionClass_ReadOnly(t *testing.T) {
	steps := []Step{
		{Tool: "check_connection"},
		{Tool: "get_table_stats"},
	}
	got := jobActionClass(steps)
	if got != audit.ActionRead {
		t.Errorf("jobActionClass = %q, want %q", got, audit.ActionRead)
	}
}

func TestJobActionClass_WriteStep(t *testing.T) {
	steps := []Step{
		{Tool: "check_connection"},
		{Tool: "cancel_query"},
	}
	got := jobActionClass(steps)
	if got != audit.ActionWrite {
		t.Errorf("jobActionClass = %q, want %q", got, audit.ActionWrite)
	}
}

func TestJobActionClass_DestructiveStep(t *testing.T) {
	steps := []Step{
		{Tool: "get_running_queries"},
		{Tool: "terminate_connection"},
	}
	got := jobActionClass(steps)
	if got != audit.ActionDestructive {
		t.Errorf("jobActionClass = %q, want %q", got, audit.ActionDestructive)
	}
}

func TestJobActionClass_Mixed(t *testing.T) {
	steps := []Step{
		{Tool: "check_connection"},  // read
		{Tool: "cancel_query"},      // write
		{Tool: "delete_pod"},        // destructive
	}
	got := jobActionClass(steps)
	if got != audit.ActionDestructive {
		t.Errorf("jobActionClass = %q, want %q", got, audit.ActionDestructive)
	}
}
