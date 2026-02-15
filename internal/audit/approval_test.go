package audit

import (
	"testing"
	"time"
)

func TestDefaultPolicy_AutoApproveReads(t *testing.T) {
	policy := DefaultPolicy()

	approval := policy.Evaluate(ApprovalRequest{
		ActionClass: ActionRead,
		Agent:       "postgres_database_agent",
		Tool:        "check_connection",
	})

	if approval.Required {
		t.Error("read operations should not require approval")
	}
	if approval.Status != ApprovalAutoApproved {
		t.Errorf("status = %q, want %q", approval.Status, ApprovalAutoApproved)
	}
}

func TestDefaultPolicy_RequireApprovalDestructive(t *testing.T) {
	policy := DefaultPolicy()

	approval := policy.Evaluate(ApprovalRequest{
		ActionClass: ActionDestructive,
		Agent:       "postgres_database_agent",
		Tool:        "kill_query",
		Principal:   "boris",
	})

	if !approval.Required {
		t.Error("destructive operations should require approval")
	}
	if approval.Status != ApprovalPending {
		t.Errorf("status = %q, want %q", approval.Status, ApprovalPending)
	}
	if approval.RequestedBy != "boris" {
		t.Errorf("requested_by = %q, want %q", approval.RequestedBy, "boris")
	}
}

func TestDefaultPolicy_RequireApprovalProdWrites(t *testing.T) {
	policy := DefaultPolicy()

	// Prod environment should require approval for writes
	approval := policy.Evaluate(ApprovalRequest{
		ActionClass: ActionWrite,
		Agent:       "k8s_agent",
		Tool:        "scale_deployment",
		Environment: "prod",
	})

	if !approval.Required {
		t.Error("write operations in prod should require approval")
	}
	if approval.Status != ApprovalPending {
		t.Errorf("status = %q, want %q", approval.Status, ApprovalPending)
	}
}

func TestDefaultPolicy_AutoApproveNonProdWrites(t *testing.T) {
	policy := DefaultPolicy()

	// Non-prod environment should auto-approve writes
	approval := policy.Evaluate(ApprovalRequest{
		ActionClass: ActionWrite,
		Agent:       "k8s_agent",
		Tool:        "scale_deployment",
		Environment: "staging",
	})

	if approval.Required {
		t.Error("write operations in non-prod should not require approval")
	}
	if approval.Status != ApprovalAutoApproved {
		t.Errorf("status = %q, want %q", approval.Status, ApprovalAutoApproved)
	}
}

func TestApprovalManager_ApproveAndDeny(t *testing.T) {
	manager := NewApprovalManager(nil)

	// Create a pending approval
	pending := manager.RequestApproval(
		"evt_123",
		"tr_abc",
		"Kill long-running query",
		ApprovalRequest{
			ActionClass: ActionDestructive,
			Agent:       "postgres_database_agent",
			Tool:        "kill_query",
			Principal:   "boris",
		},
	)

	if pending.Approval.Status != ApprovalPending {
		t.Errorf("initial status = %q, want %q", pending.Approval.Status, ApprovalPending)
	}

	// Verify it's in pending list
	pendingList := manager.GetPending()
	if len(pendingList) != 1 {
		t.Errorf("pending count = %d, want 1", len(pendingList))
	}

	// Approve it
	err := manager.Approve("evt_123", "admin", "Approved to fix incident #123", 5*time.Minute)
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}

	if pending.Approval.Status != ApprovalApproved {
		t.Errorf("after approve status = %q, want %q", pending.Approval.Status, ApprovalApproved)
	}
	if pending.Approval.ApprovedBy != "admin" {
		t.Errorf("approved_by = %q, want %q", pending.Approval.ApprovedBy, "admin")
	}
	if pending.Approval.Justification != "Approved to fix incident #123" {
		t.Errorf("justification = %q, want approval reason", pending.Approval.Justification)
	}
	if pending.Approval.ExpiresAt.IsZero() {
		t.Error("expires_at should be set")
	}

	// Pending list should be empty now
	pendingList = manager.GetPending()
	if len(pendingList) != 0 {
		t.Errorf("pending count after approval = %d, want 0", len(pendingList))
	}
}

func TestApprovalManager_Deny(t *testing.T) {
	manager := NewApprovalManager(nil)

	// Create a pending approval
	pending := manager.RequestApproval(
		"evt_456",
		"tr_def",
		"Drop production table",
		ApprovalRequest{
			ActionClass: ActionDestructive,
			Agent:       "postgres_database_agent",
			Tool:        "run_query",
			Environment: "prod",
			Principal:   "unknown",
		},
	)

	// Deny it
	err := manager.Deny("evt_456", "security-team", "Suspicious activity from unknown principal")
	if err != nil {
		t.Fatalf("deny failed: %v", err)
	}

	if pending.Approval.Status != ApprovalDenied {
		t.Errorf("after deny status = %q, want %q", pending.Approval.Status, ApprovalDenied)
	}
	if pending.Approval.ApprovedBy != "security-team" {
		t.Errorf("approved_by = %q, want %q", pending.Approval.ApprovedBy, "security-team")
	}
}

func TestApproval_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		approval Approval
		want     bool
	}{
		{
			name: "approved is valid",
			approval: Approval{
				Status: ApprovalApproved,
			},
			want: true,
		},
		{
			name: "auto-approved is valid",
			approval: Approval{
				Status: ApprovalAutoApproved,
			},
			want: true,
		},
		{
			name: "pending is not valid",
			approval: Approval{
				Status: ApprovalPending,
			},
			want: false,
		},
		{
			name: "denied is not valid",
			approval: Approval{
				Status: ApprovalDenied,
			},
			want: false,
		},
		{
			name: "expired approval is not valid",
			approval: Approval{
				Status:    ApprovalApproved,
				ExpiresAt: time.Now().Add(-1 * time.Hour),
			},
			want: false,
		},
		{
			name: "future expiry is valid",
			approval: Approval{
				Status:    ApprovalApproved,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.approval.IsValid()
			if got != tc.want {
				t.Errorf("IsValid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApprovalRule_Matches(t *testing.T) {
	tests := []struct {
		name    string
		rule    ApprovalRule
		request ApprovalRequest
		want    bool
	}{
		{
			name: "match action class",
			rule: ApprovalRule{
				ActionClasses: []ActionClass{ActionDestructive},
			},
			request: ApprovalRequest{ActionClass: ActionDestructive},
			want:    true,
		},
		{
			name: "no match action class",
			rule: ApprovalRule{
				ActionClasses: []ActionClass{ActionDestructive},
			},
			request: ApprovalRequest{ActionClass: ActionRead},
			want:    false,
		},
		{
			name: "match multiple conditions",
			rule: ApprovalRule{
				ActionClasses: []ActionClass{ActionWrite},
				Environments:  []string{"prod", "production"},
			},
			request: ApprovalRequest{
				ActionClass: ActionWrite,
				Environment: "prod",
			},
			want: true,
		},
		{
			name: "partial match fails",
			rule: ApprovalRule{
				ActionClasses: []ActionClass{ActionWrite},
				Environments:  []string{"prod"},
			},
			request: ApprovalRequest{
				ActionClass: ActionWrite,
				Environment: "staging",
			},
			want: false,
		},
		{
			name: "empty rule matches everything",
			rule: ApprovalRule{},
			request: ApprovalRequest{
				ActionClass: ActionDestructive,
				Agent:       "any_agent",
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rule.matches(tc.request)
			if got != tc.want {
				t.Errorf("matches() = %v, want %v", got, tc.want)
			}
		})
	}
}
