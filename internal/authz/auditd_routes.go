package authz

// DefaultAuditdPermissions is the single source of truth for authorization on
// the auditd service. Keys are Go 1.22 ServeMux patterns exactly as registered
// in cmd/auditd/main.go.
var DefaultAuditdPermissions = map[string]Permission{
	// ── Public ────────────────────────────────────────────────────────────────
	"GET /health": {AllowAnonymous: true},

	// ── Authenticated reads: any verified user ────────────────────────────────
	"GET /v1/events":                                         {AdminBypass: true},
	"GET /v1/events/{eventID}":                              {AdminBypass: true},
	"GET /v1/verify":                                        {AdminBypass: true},
	"GET /v1/journeys":                                      {AdminBypass: true},
	"GET /v1/approvals":                                     {AdminBypass: true},
	"GET /v1/approvals/pending":                             {AdminBypass: true},
	"GET /v1/approvals/{approvalID}":                        {AdminBypass: true},
	"GET /v1/approvals/{approvalID}/wait":                   {AdminBypass: true},
	"GET /v1/governance/info":                               {AdminBypass: true},
	"GET /v1/governance/policies":                           {AdminBypass: true},
	"GET /v1/governance/explain":                            {AdminBypass: true},
	"GET /v1/govbot/runs":                                   {AdminBypass: true},
	"GET /v1/fleet/jobs":                                    {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}":                            {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}/servers":                    {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}/servers/{serverName}":       {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}/servers/{serverName}/steps": {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}/approval/{approvalID}":      {AdminBypass: true},

	// Playbook reads
	"GET /v1/fleet/playbooks":              {AdminBypass: true},
	"GET /v1/fleet/playbooks/{playbookID}": {AdminBypass: true},

	// ── Service-only writes: machine-to-machine paths ─────────────────────────
	// These endpoints are called by agents and fleet-runner service accounts,
	// not by human users. ServiceOnly rejects human callers even in enforcing mode.

	// Audit event writes (called by gateway's GatewayAuditor and agents)
	"POST /v1/events":                   {ServiceOnly: true, AdminBypass: true},
	"POST /v1/events/{eventID}/outcome": {ServiceOnly: true, AdminBypass: true},

	// Approval creation (called by agents when a policy requires approval)
	"POST /v1/approvals": {ServiceOnly: true, AdminBypass: true},

	// Policy check (called by agents for pre-flight governance evaluation)
	"POST /v1/governance/check": {ServiceOnly: true, AdminBypass: true},

	// Govbot compliance history write
	"POST /v1/govbot/runs": {ServiceOnly: true, AdminBypass: true},

	// Playbook writes (fleet-operator service accounts or admin)
	"POST /v1/fleet/playbooks":              {ServiceOnly: true, AdminBypass: true},
	"DELETE /v1/fleet/playbooks/{playbookID}": {ServiceOnly: true, AdminBypass: true},

	// Fleet-runner lifecycle writes
	"POST /v1/fleet/jobs":                                                   {ServiceOnly: true, AdminBypass: true},
	"PATCH /v1/fleet/jobs/{jobID}/status":                                   {ServiceOnly: true, AdminBypass: true},
	"POST /v1/fleet/jobs/{jobID}/servers":                                   {ServiceOnly: true, AdminBypass: true},
	"PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}":                     {ServiceOnly: true, AdminBypass: true},
	"POST /v1/fleet/jobs/{jobID}/servers/{serverName}/steps":                {ServiceOnly: true, AdminBypass: true},
	"PATCH /v1/fleet/jobs/{jobID}/servers/{serverName}/steps/{stepIndex}":   {ServiceOnly: true, AdminBypass: true},
	"POST /v1/fleet/jobs/{jobID}/approval":                                  {ServiceOnly: true, AdminBypass: true},

	// ── Role-required: human approval actions ─────────────────────────────────

	// Coarse middleware gate: dba OR fleet-approver passes through.
	// The handler then calls authzr.Require() with the specific role for the
	// approval type (fleet job → fleet-approver only; db action → dba only).
	"POST /v1/approvals/{approvalID}/approve": {
		RequireRoles: []string{"dba", "fleet-approver"},
		AdminBypass:  true,
	},
	"POST /v1/approvals/{approvalID}/deny": {
		RequireRoles: []string{"dba", "fleet-approver"},
		AdminBypass:  true,
	},

	// Cancel: any authenticated caller (ownership/requester check is in the handler).
	"POST /v1/approvals/{approvalID}/cancel": {AdminBypass: true},
}
