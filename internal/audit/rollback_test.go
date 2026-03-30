package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- DeriveRollbackPlan tests ---

func TestDeriveRollbackPlan_ScaleDeployment_Success(t *testing.T) {
	pre, _ := json.Marshal(ScalePreState{
		Namespace:        "production",
		DeploymentName:   "api",
		PreviousReplicas: 3,
	})
	event := &Event{
		EventID: "tool_abc12345",
		TraceID: "sess_trace1",
		Tool: &ToolExecution{
			Name:     "scale_deployment",
			PreState: pre,
		},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes", plan.Reversibility)
	}
	if plan.InverseOp == nil {
		t.Fatal("InverseOp is nil, want non-nil")
	}
	if plan.InverseOp.Tool != "scale_deployment" {
		t.Errorf("InverseOp.Tool = %q, want scale_deployment", plan.InverseOp.Tool)
	}
	if plan.InverseOp.Args["replicas"] != 3 {
		t.Errorf("InverseOp.Args[replicas] = %v, want 3", plan.InverseOp.Args["replicas"])
	}
	if plan.InverseOp.Args["namespace"] != "production" {
		t.Errorf("InverseOp.Args[namespace] = %v, want production", plan.InverseOp.Args["namespace"])
	}
	if plan.OriginalEventID != "tool_abc12345" {
		t.Errorf("OriginalEventID = %q, want tool_abc12345", plan.OriginalEventID)
	}
}

func TestDeriveRollbackPlan_ScaleDeployment_NoPreState(t *testing.T) {
	event := &Event{
		EventID: "tool_nostate",
		Tool:    &ToolExecution{Name: "scale_deployment"},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityNo {
		t.Errorf("Reversibility = %q, want no (no pre-state captured)", plan.Reversibility)
	}
	if !strings.Contains(plan.NotReversibleReason, "Pre-mutation state was not captured") {
		t.Errorf("NotReversibleReason = %q, want pre-state missing message", plan.NotReversibleReason)
	}
}

func TestDeriveRollbackPlan_ScaleDeployment_ZeroReplicas(t *testing.T) {
	// PreviousReplicas=0 is considered unsafe to restore (was already at 0).
	pre, _ := json.Marshal(ScalePreState{
		Namespace:        "default",
		DeploymentName:   "worker",
		PreviousReplicas: 0,
	})
	event := &Event{
		EventID: "tool_zero",
		Tool:    &ToolExecution{Name: "scale_deployment", PreState: pre},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityNo {
		t.Errorf("Reversibility = %q, want no (zero replicas)", plan.Reversibility)
	}
}

func TestDeriveRollbackPlan_DeletePod(t *testing.T) {
	event := &Event{
		EventID: "tool_delpod",
		Tool:    &ToolExecution{Name: "delete_pod"},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityPartial {
		t.Errorf("Reversibility = %q, want partial", plan.Reversibility)
	}
	if plan.InverseOp != nil {
		t.Error("InverseOp should be nil for partial reversibility")
	}
}

func TestDeriveRollbackPlan_RestartDeployment(t *testing.T) {
	event := &Event{
		EventID: "tool_restart",
		Tool:    &ToolExecution{Name: "restart_deployment"},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityNo {
		t.Errorf("Reversibility = %q, want no", plan.Reversibility)
	}
}

func TestDeriveRollbackPlan_TerminateConnection(t *testing.T) {
	for _, toolName := range []string{"terminate_connection", "terminate_idle_connections", "cancel_query"} {
		t.Run(toolName, func(t *testing.T) {
			event := &Event{
				EventID: "tool_term",
				Tool:    &ToolExecution{Name: toolName},
			}
			plan, err := DeriveRollbackPlan(event)
			if err != nil {
				t.Fatalf("DeriveRollbackPlan() error = %v", err)
			}
			if plan.Reversibility != ReversibilityNo {
				t.Errorf("Reversibility = %q, want no", plan.Reversibility)
			}
			if plan.InverseOp != nil {
				t.Error("InverseOp should be nil for irreversible ops")
			}
		})
	}
}

func TestDeriveRollbackPlan_NilTool(t *testing.T) {
	event := &Event{EventID: "tool_nil"}
	_, err := DeriveRollbackPlan(event)
	if err == nil {
		t.Error("expected error for nil Tool, got nil")
	}
}

// --- DML rollback derivation ---

func TestDeriveRollbackPlan_ExecUpdate_Tier1(t *testing.T) {
	pre, _ := json.Marshal(DMLPreState{
		Schema:    "public",
		Table:     "orders",
		Operation: "update",
		PKColumns: []string{"id"},
		Rows: []map[string]any{
			{"id": 1, "status": "pending"},
			{"id": 2, "status": "pending"},
		},
		RowCount: 2,
		Tier:     1,
	})
	event := &Event{
		EventID: "tool_upd1",
		Tool:    &ToolExecution{Name: "exec_update", PreState: pre},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes", plan.Reversibility)
	}
	if plan.InverseOp == nil {
		t.Fatal("InverseOp is nil")
	}
	sql, ok := plan.InverseOp.Args["sql"].(string)
	if !ok {
		t.Fatal("InverseOp.Args[sql] is not a string")
	}
	if !strings.Contains(sql, "UPDATE orders") {
		t.Errorf("sql = %q, want UPDATE orders", sql)
	}
	if !strings.Contains(sql, "status = pending") {
		t.Errorf("sql = %q, want status = pending", sql)
	}
}

func TestDeriveRollbackPlan_ExecDelete_Tier1(t *testing.T) {
	pre, _ := json.Marshal(DMLPreState{
		Schema:    "public",
		Table:     "sessions",
		Operation: "delete",
		PKColumns: []string{"session_id"},
		Rows: []map[string]any{
			{"session_id": "abc", "user_id": 42, "created_at": "2024-01-01"},
		},
		RowCount: 1,
		Tier:     1,
	})
	event := &Event{
		EventID: "tool_del1",
		Tool:    &ToolExecution{Name: "exec_delete", PreState: pre},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes", plan.Reversibility)
	}
	sql, _ := plan.InverseOp.Args["sql"].(string)
	if !strings.Contains(sql, "INSERT INTO sessions") {
		t.Errorf("sql = %q, want INSERT INTO sessions", sql)
	}
}

func TestDeriveRollbackPlan_ExecInsert_Tier1(t *testing.T) {
	pre, _ := json.Marshal(DMLPreState{
		Schema:      "public",
		Table:       "tags",
		Operation:   "insert",
		PKColumns:   []string{"id"},
		InsertedPKs: []map[string]any{{"id": 101}, {"id": 102}},
		RowCount:    2,
		Tier:        1,
	})
	event := &Event{
		EventID: "tool_ins1",
		Tool:    &ToolExecution{Name: "exec_insert", PreState: pre},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	if plan.Reversibility != ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes", plan.Reversibility)
	}
	sql, _ := plan.InverseOp.Args["sql"].(string)
	if !strings.Contains(sql, "DELETE FROM tags") {
		t.Errorf("sql = %q, want DELETE FROM tags", sql)
	}
	if !strings.Contains(sql, "101") && !strings.Contains(sql, "id IN") {
		t.Errorf("sql = %q, want pk values in DELETE", sql)
	}
}

func TestDeriveRollbackPlan_ExecUpdate_ZeroRows(t *testing.T) {
	pre, _ := json.Marshal(DMLPreState{
		Schema:    "public",
		Table:     "orders",
		Operation: "update",
		PKColumns: []string{"id"},
		RowCount:  0,
		Tier:      1,
	})
	event := &Event{
		EventID: "tool_upd0",
		Tool:    &ToolExecution{Name: "exec_update", PreState: pre},
	}
	plan, err := DeriveRollbackPlan(event)
	if err != nil {
		t.Fatalf("DeriveRollbackPlan() error = %v", err)
	}
	// Zero rows = nothing to undo; still Reversibility=yes but no-op SQL.
	if plan.Reversibility != ReversibilityYes {
		t.Errorf("Reversibility = %q, want yes (no-op)", plan.Reversibility)
	}
}

// --- RollbackStore CRUD tests ---

func newTestRollbackStore(t *testing.T) *RollbackStore {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "rollback_test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	s, err := NewRollbackStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewRollbackStore: %v", err)
	}
	return s
}

func newTestAuditStoreForRollback(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "audit_rollback_test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRollbackStore_CreateAndGet(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	plan := RollbackPlan{
		OriginalEventID: "tool_abc",
		OriginalTool:    "scale_deployment",
		Reversibility:   ReversibilityYes,
	}
	planJSON, _ := json.Marshal(plan)

	r := &RollbackRecord{
		OriginalEventID: "tool_abc",
		OriginalTraceID: "sess_trace",
		Status:          "pending_approval",
		InitiatedBy:     "alice",
		PlanJSON:        string(planJSON),
	}
	if err := s.CreateRollback(ctx, r); err != nil {
		t.Fatalf("CreateRollback: %v", err)
	}
	if r.RollbackID == "" {
		t.Error("RollbackID not set after create")
	}
	if r.RollbackTraceID == "" {
		t.Error("RollbackTraceID not set after create")
	}
	wantTraceID := "tr_" + r.RollbackID
	if r.RollbackTraceID != wantTraceID {
		t.Errorf("RollbackTraceID = %q, want %q (tr_ + RollbackID)", r.RollbackTraceID, wantTraceID)
	}

	got, err := s.GetRollback(ctx, r.RollbackID)
	if err != nil {
		t.Fatalf("GetRollback: %v", err)
	}
	if got.OriginalEventID != "tool_abc" {
		t.Errorf("OriginalEventID = %q, want tool_abc", got.OriginalEventID)
	}
	if got.Status != "pending_approval" {
		t.Errorf("Status = %q, want pending_approval", got.Status)
	}
	if got.InitiatedBy != "alice" {
		t.Errorf("InitiatedBy = %q, want alice", got.InitiatedBy)
	}
}

func TestRollbackStore_UpdateStatus(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	r := &RollbackRecord{
		OriginalEventID: "tool_upd",
		InitiatedBy:     "bob",
		PlanJSON:        `{}`,
	}
	if err := s.CreateRollback(ctx, r); err != nil {
		t.Fatalf("CreateRollback: %v", err)
	}

	if err := s.UpdateRollbackStatus(ctx, r.RollbackID, "executing", ""); err != nil {
		t.Fatalf("UpdateRollbackStatus: %v", err)
	}
	got, _ := s.GetRollback(ctx, r.RollbackID)
	if got.Status != "executing" {
		t.Errorf("Status = %q, want executing", got.Status)
	}
	if !got.CompletedAt.IsZero() {
		t.Error("CompletedAt should be zero for non-terminal status")
	}

	if err := s.UpdateRollbackStatus(ctx, r.RollbackID, "success", "deployment restored"); err != nil {
		t.Fatalf("UpdateRollbackStatus to success: %v", err)
	}
	got, _ = s.GetRollback(ctx, r.RollbackID)
	if got.Status != "success" {
		t.Errorf("Status = %q, want success", got.Status)
	}
	if got.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set for terminal status")
	}
	if got.ResultOutput != "deployment restored" {
		t.Errorf("ResultOutput = %q, want 'deployment restored'", got.ResultOutput)
	}
}

func TestRollbackStore_GetByEventID_NoneExists(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	got, err := s.GetRollbackByEventID(ctx, "tool_nonexistent")
	if err != nil {
		t.Fatalf("GetRollbackByEventID: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestRollbackStore_GetByEventID_SkipsTerminated(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	// Create a failed rollback; should not be returned by GetRollbackByEventID.
	r := &RollbackRecord{
		OriginalEventID: "tool_old",
		InitiatedBy:     "alice",
		Status:          "failed",
		PlanJSON:        `{}`,
	}
	if err := s.CreateRollback(ctx, r); err != nil {
		t.Fatalf("CreateRollback: %v", err)
	}

	got, err := s.GetRollbackByEventID(ctx, "tool_old")
	if err != nil {
		t.Fatalf("GetRollbackByEventID: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (failed rollback), got %+v", got)
	}
}

func TestRollbackStore_List(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	for _, status := range []string{"pending_approval", "success", "failed"} {
		r := &RollbackRecord{
			OriginalEventID: "tool_" + status,
			InitiatedBy:     "alice",
			Status:          status,
			PlanJSON:        `{}`,
		}
		if err := s.CreateRollback(ctx, r); err != nil {
			t.Fatalf("CreateRollback(%s): %v", status, err)
		}
	}

	all, err := s.ListRollbacks(ctx, RollbackQueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListRollbacks: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len(all) = %d, want 3", len(all))
	}

	pending, err := s.ListRollbacks(ctx, RollbackQueryOptions{Status: "pending_approval"})
	if err != nil {
		t.Fatalf("ListRollbacks(pending): %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("len(pending) = %d, want 1", len(pending))
	}
}

func TestRollbackStore_FleetRollback_CreateAndGet(t *testing.T) {
	s := newTestRollbackStore(t)
	ctx := context.Background()

	r := &FleetRollbackRecord{
		OriginalJobID: "flj_abc12345",
		InitiatedBy:   "carol",
		Scope:         "canary_only",
	}
	if err := s.CreateFleetRollback(ctx, r); err != nil {
		t.Fatalf("CreateFleetRollback: %v", err)
	}
	if r.FleetRollbackID == "" {
		t.Error("FleetRollbackID not set")
	}

	got, err := s.GetFleetRollback(ctx, r.FleetRollbackID)
	if err != nil {
		t.Fatalf("GetFleetRollback: %v", err)
	}
	if got.OriginalJobID != "flj_abc12345" {
		t.Errorf("OriginalJobID = %q, want flj_abc12345", got.OriginalJobID)
	}
	if got.Scope != "canary_only" {
		t.Errorf("Scope = %q, want canary_only", got.Scope)
	}

	if err := s.UpdateFleetRollbackStatus(ctx, r.FleetRollbackID, "executing", "flj_rbk11"); err != nil {
		t.Fatalf("UpdateFleetRollbackStatus: %v", err)
	}
	got, _ = s.GetFleetRollback(ctx, r.FleetRollbackID)
	if got.Status != "executing" {
		t.Errorf("Status = %q, want executing", got.Status)
	}
	if got.RollbackJobID != "flj_rbk11" {
		t.Errorf("RollbackJobID = %q, want flj_rbk11", got.RollbackJobID)
	}
}

// --- ToolCall.PreState threading test ---

func TestToolAuditor_RecordToolCall_PreState(t *testing.T) {
	store := newTestAuditStoreForRollback(t)
	ta := NewToolAuditor(store, "k8s_agent", "sess_test", "trace_test")

	pre, _ := json.Marshal(ScalePreState{
		Namespace:        "default",
		DeploymentName:   "web",
		PreviousReplicas: 3,
	})

	ta.RecordToolCall(context.Background(), ToolCall{
		Name:       "scale_deployment",
		Parameters: map[string]any{"replicas": 5},
		RawCommand: "kubectl scale deployment web --replicas 5",
		PreState:   pre,
	}, ToolResult{Output: `deployment.apps "web" scaled`}, 10*time.Millisecond)

	events, err := store.Query(context.Background(), QueryOptions{ToolName: "scale_deployment"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events found")
	}
	ev := events[0]
	if ev.Tool == nil {
		t.Fatal("Tool is nil")
	}
	if len(ev.Tool.PreState) == 0 {
		t.Error("PreState is empty, want ScalePreState JSON")
	}
	var got ScalePreState
	if err := json.Unmarshal(ev.Tool.PreState, &got); err != nil {
		t.Fatalf("unmarshal PreState: %v", err)
	}
	if got.PreviousReplicas != 3 {
		t.Errorf("PreviousReplicas = %d, want 3", got.PreviousReplicas)
	}
}
