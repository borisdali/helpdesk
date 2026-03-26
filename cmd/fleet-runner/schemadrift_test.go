package main

import (
	"strings"
	"testing"
	"time"

	"helpdesk/internal/fleet"
	"helpdesk/internal/toolregistry"
)

var testCapturedAt = time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)

func makeSnapshot(fp, ver string) fleet.ToolSnapshot {
	return fleet.ToolSnapshot{
		SchemaFingerprint: fp,
		AgentVersion:      ver,
		CapturedAt:        testCapturedAt,
	}
}

func makeEntry(name, fp, ver string) toolregistry.ToolEntry {
	return toolregistry.ToolEntry{
		Name:              name,
		SchemaFingerprint: fp,
		AgentVersion:      ver,
	}
}

func TestCheckSchemaDrift_NoChange(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"get_status_summary": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "a3f9c2", "0.5.0")}

	results, err := CheckSchemaDrift(snapshots, live, "abort")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCheckSchemaDrift_VersionOnly(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"get_status_summary": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "a3f9c2", "0.6.0")}

	results, err := CheckSchemaDrift(snapshots, live, "abort")
	if err != nil {
		t.Errorf("unexpected error for version-only change: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FingerprintChanged {
		t.Error("FingerprintChanged = true, want false")
	}
	if !results[0].VersionChanged {
		t.Error("VersionChanged = false, want true")
	}
}

func TestCheckSchemaDrift_FingerprintChanged_Abort(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"get_status_summary": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "def456", "0.6.0")}

	results, err := CheckSchemaDrift(snapshots, live, "abort")
	if err == nil {
		t.Error("expected error for fingerprint change with abort policy, got nil")
	}
	if !strings.Contains(err.Error(), "schema drift detected") {
		t.Errorf("error message missing 'schema drift detected': %v", err)
	}
	if !strings.Contains(err.Error(), "aborting") {
		t.Errorf("error message missing 'aborting': %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result even on error, got %d", len(results))
	}
}

func TestCheckSchemaDrift_FingerprintChanged_Warn(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"get_status_summary": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "def456", "0.6.0")}

	results, err := CheckSchemaDrift(snapshots, live, "warn")
	if err != nil {
		t.Errorf("unexpected error for warn policy: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].FingerprintChanged {
		t.Error("FingerprintChanged = false, want true")
	}
}

func TestCheckSchemaDrift_FingerprintChanged_Ignore(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"get_status_summary": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "def456", "0.6.0")}

	results, err := CheckSchemaDrift(snapshots, live, "ignore")
	if err != nil {
		t.Errorf("unexpected error for ignore policy: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results for ignore policy, got %d", len(results))
	}
}

func TestCheckSchemaDrift_NoSnapshots_Abort(t *testing.T) {
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "a3f9c2", "0.5.0")}

	_, err := CheckSchemaDrift(nil, live, "abort")
	if err == nil {
		t.Error("expected error for nil snapshots with abort policy, got nil")
	}
	if !strings.Contains(err.Error(), "no tool_snapshots") {
		t.Errorf("error message missing 'no tool_snapshots': %v", err)
	}
}

func TestCheckSchemaDrift_NoSnapshots_Ignore(t *testing.T) {
	live := []toolregistry.ToolEntry{makeEntry("get_status_summary", "a3f9c2", "0.5.0")}

	results, err := CheckSchemaDrift(nil, live, "ignore")
	if err != nil {
		t.Errorf("unexpected error for nil snapshots with ignore policy: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCheckSchemaDrift_ToolRemovedFromRegistry(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"deleted_tool": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{} // tool no longer exists

	results, err := CheckSchemaDrift(snapshots, live, "warn")
	if err != nil {
		t.Errorf("unexpected error for warn policy: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Tool != "deleted_tool" {
		t.Errorf("Tool = %q, want %q", results[0].Tool, "deleted_tool")
	}
}

func TestCheckSchemaDrift_ToolRemovedFromRegistry_Abort(t *testing.T) {
	snapshots := map[string]fleet.ToolSnapshot{
		"deleted_tool": makeSnapshot("a3f9c2", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{}

	_, err := CheckSchemaDrift(snapshots, live, "abort")
	if err == nil {
		t.Error("expected error when tool removed with abort policy")
	}
}

func TestCheckSchemaDrift_EmptyFingerprintsSkipped(t *testing.T) {
	// If either snapshot or live fingerprint is empty, no drift is detected
	// (tool may not support schema fingerprinting).
	snapshots := map[string]fleet.ToolSnapshot{
		"old_tool": makeSnapshot("", "0.5.0"),
	}
	live := []toolregistry.ToolEntry{makeEntry("old_tool", "", "0.5.0")}

	results, err := CheckSchemaDrift(snapshots, live, "abort")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no drift for empty fingerprints, got %d results", len(results))
	}
}
