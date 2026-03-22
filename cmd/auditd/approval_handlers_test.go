package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"helpdesk/internal/audit"
	"helpdesk/internal/identity"
)

// testUsersYAML defines a minimal set of users covering all roles used in the
// authorization tests. X-User header auth is used throughout (no Argon2id
// hashing needed, which keeps the tests fast).
const testUsersYAML = `
users:
  - id: alice@example.com
    roles: [dba]
  - id: bob@example.com
    roles: [fleet-approver]
  - id: charlie@example.com
    roles: [operator]
  - id: admin@example.com
    roles: [admin]
`

// ── helpers ───────────────────────────────────────────────────────────────────

// newApprovalSrv creates an approvalServer backed by a fresh temp-dir SQLite
// store. Pass usersYAML to enable role-based auth; pass "" for legacy mode.
func newApprovalSrv(t *testing.T, usersYAML string) *approvalServer {
	t.Helper()
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	as, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
	}

	var provider identity.Provider
	if usersYAML != "" {
		path := filepath.Join(t.TempDir(), "users.yaml")
		if err := os.WriteFile(path, []byte(usersYAML), 0600); err != nil {
			t.Fatalf("write users.yaml: %v", err)
		}
		p, err := identity.NewStaticProvider(path)
		if err != nil {
			t.Fatalf("NewStaticProvider: %v", err)
		}
		provider = p
	}

	return &approvalServer{store: as, identityProvider: provider}
}

// seedApproval inserts an approval record directly into the store and returns
// the assigned ApprovalID.
func seedApproval(t *testing.T, s *approvalServer, a *audit.StoredApproval) string {
	t.Helper()
	if a.ExpiresAt.IsZero() {
		a.ExpiresAt = time.Now().UTC().Add(time.Hour)
	}
	if err := s.store.CreateRequest(context.Background(), a); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	return a.ApprovalID
}

// doApprove sends POST /v1/approvals/{id}/approve with the given JSON body and
// optional extra headers; returns the response recorder.
func doApprove(t *testing.T, s *approvalServer, approvalID string, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/approvals/"+approvalID+"/approve", bytes.NewReader(data))
	req.SetPathValue("approvalID", approvalID)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleApprove(w, req)
	return w
}

// doDeny sends POST /v1/approvals/{id}/deny with the given JSON body and
// optional extra headers; returns the response recorder.
func doDeny(t *testing.T, s *approvalServer, approvalID string, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/approvals/"+approvalID+"/deny", bytes.NewReader(data))
	req.SetPathValue("approvalID", approvalID)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleDeny(w, req)
	return w
}

// mutationApproval returns a pending mutation approval record requested by the
// given submitter.
func mutationApproval(submittedBy string) *audit.StoredApproval {
	return &audit.StoredApproval{
		ActionClass:  "write",
		AgentName:    "database",
		ResourceType: "database",
		ResourceName: "prod-db-1",
		RequestedBy:  submittedBy,
	}
}

// fleetApproval returns a pending fleet-job approval record requested by the
// given submitter.
func fleetApproval(submittedBy string) *audit.StoredApproval {
	return &audit.StoredApproval{
		ActionClass:  "write",
		AgentName:    "fleet-runner",
		ResourceType: "fleet_job",
		ResourceName: "job-abc",
		RequestedBy:  submittedBy,
	}
}

// ── Legacy mode (no identity provider) ───────────────────────────────────────

func TestHandleApprove_Legacy_OK(t *testing.T) {
	s := newApprovalSrv(t, "")
	id := seedApproval(t, s, mutationApproval("some-operator"))

	w := doApprove(t, s, id, map[string]any{"approved_by": "alice"}, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp audit.StoredApproval
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Status != "approved" {
		t.Errorf("status = %q, want approved", resp.Status)
	}
	if resp.ResolvedBy != "alice" {
		t.Errorf("resolved_by = %q, want alice", resp.ResolvedBy)
	}
}

func TestHandleApprove_Legacy_MissingApprovedBy(t *testing.T) {
	s := newApprovalSrv(t, "")
	id := seedApproval(t, s, mutationApproval("some-operator"))

	w := doApprove(t, s, id, map[string]any{}, nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleDeny_Legacy_OK(t *testing.T) {
	s := newApprovalSrv(t, "")
	id := seedApproval(t, s, mutationApproval("some-operator"))

	w := doDeny(t, s, id, map[string]any{"denied_by": "alice", "reason": "not approved"}, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp audit.StoredApproval
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.Status != "denied" {
		t.Errorf("status = %q, want denied", resp.Status)
	}
	if resp.ResolvedBy != "alice" {
		t.Errorf("resolved_by = %q, want alice", resp.ResolvedBy)
	}
}

func TestHandleDeny_Legacy_MissingDeniedBy(t *testing.T) {
	s := newApprovalSrv(t, "")
	id := seedApproval(t, s, mutationApproval("some-operator"))

	w := doDeny(t, s, id, map[string]any{"reason": "no"}, nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ── Auth mode — credential failures ──────────────────────────────────────────

func TestHandleApprove_Auth_NoCredentials(t *testing.T) {
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("charlie@example.com"))

	// No X-User header, no Bearer token.
	w := doApprove(t, s, id, map[string]any{"approved_by": "alice@example.com"}, nil)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleApprove_Auth_UnknownUser(t *testing.T) {
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "nobody@example.com"})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ── Auth mode — role failures ─────────────────────────────────────────────────

func TestHandleApprove_Auth_WrongRole_Mutation(t *testing.T) {
	// charlie has role [operator] — cannot approve any mutation.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("someone-else"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "charlie@example.com"})

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestHandleApprove_Auth_WrongRole_Fleet(t *testing.T) {
	// alice has role [dba] — cannot approve fleet jobs; fleet-approver is required.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, fleetApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "alice@example.com"})

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// ── Auth mode — happy paths ───────────────────────────────────────────────────

func TestHandleApprove_Auth_DBA_Mutation(t *testing.T) {
	// alice (dba) approves a mutation; body's approved_by must be overridden.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("charlie@example.com"))

	// Intentionally pass a different approved_by in the body — server must
	// ignore it and use the verified principal.
	w := doApprove(t, s, id, map[string]any{"approved_by": "impostor"}, map[string]string{"X-User": "alice@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp audit.StoredApproval
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.ResolvedBy != "alice@example.com" {
		t.Errorf("resolved_by = %q, want alice@example.com (body value must be ignored)", resp.ResolvedBy)
	}
}

func TestHandleApprove_Auth_FleetApprover_Fleet(t *testing.T) {
	// bob (fleet-approver) approves a fleet job submitted by someone else.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, fleetApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "bob@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp audit.StoredApproval
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.ResolvedBy != "bob@example.com" {
		t.Errorf("resolved_by = %q, want bob@example.com", resp.ResolvedBy)
	}
}

func TestHandleApprove_Auth_Admin_Fleet(t *testing.T) {
	// admin can approve any approval type.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, fleetApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "admin@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleApprove_Auth_Admin_Mutation(t *testing.T) {
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "admin@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeny_Auth_DBA_Mutation(t *testing.T) {
	// alice (dba) denies a mutation; denied_by must be overridden by the server.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("charlie@example.com"))

	w := doDeny(t, s, id, map[string]any{"denied_by": "impostor", "reason": "not justified"}, map[string]string{"X-User": "alice@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp audit.StoredApproval
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck
	if resp.ResolvedBy != "alice@example.com" {
		t.Errorf("resolved_by = %q, want alice@example.com", resp.ResolvedBy)
	}
	if resp.Status != "denied" {
		t.Errorf("status = %q, want denied", resp.Status)
	}
}

// ── Four-eyes enforcement ─────────────────────────────────────────────────────

func TestHandleApprove_Auth_FourEyes_Violation(t *testing.T) {
	// bob (fleet-approver) submitted the job AND is trying to approve it — must be rejected.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, fleetApproval("bob@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "bob@example.com"})

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (four-eyes violation)", w.Code)
	}
}

func TestHandleApprove_Auth_FourEyes_OK(t *testing.T) {
	// bob (fleet-approver) approves a job that charlie submitted — this is fine.
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, fleetApproval("charlie@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "bob@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleApprove_Auth_FourEyes_MutationNotEnforced(t *testing.T) {
	// Four-eyes is only enforced for fleet jobs; individual mutations by the
	// same person as requester are allowed (alice both requested and approves).
	s := newApprovalSrv(t, testUsersYAML)
	id := seedApproval(t, s, mutationApproval("alice@example.com"))

	w := doApprove(t, s, id, map[string]any{}, map[string]string{"X-User": "alice@example.com"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// ── Fleet approval record metadata ───────────────────────────────────────────

func TestHandleCreateJobApproval_SetsResourceTypeAndRole(t *testing.T) {
	store, err := audit.NewStore(audit.StoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	as, err := audit.NewApprovalStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewApprovalStore: %v", err)
	}

	fs, err := audit.NewFleetStore(store.DB(), store.IsPostgres())
	if err != nil {
		t.Fatalf("NewFleetStore: %v", err)
	}

	srv := &fleetServer{store: fs, approvalStore: as}

	body := map[string]any{
		"action_class":  "write",
		"resource_name": "my-fleet-job",
		"requested_by":  "fleet-runner",
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/jobs/flj_abc123/approval", bytes.NewReader(data))
	req.SetPathValue("jobID", "flj_abc123")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateJobApproval(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ApprovalID == "" {
		t.Fatal("expected non-empty approval_id")
	}

	// Fetch the stored record and verify the hardcoded fields.
	stored, err := as.GetRequest(context.Background(), resp.ApprovalID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if stored.ResourceType != "fleet_job" {
		t.Errorf("resource_type = %q, want fleet_job", stored.ResourceType)
	}
	if stored.ApproverRole != "fleet-approver" {
		t.Errorf("approver_role = %q, want fleet-approver", stored.ApproverRole)
	}
	if stored.AgentName != "fleet-runner" {
		t.Errorf("agent_name = %q, want fleet-runner", stored.AgentName)
	}
}
