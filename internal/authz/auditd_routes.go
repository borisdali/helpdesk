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
	"GET /v1/governance/info":                               {AllowAnonymous: true},
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

	// Playbook writes
	"POST /v1/fleet/playbooks":                         {AdminBypass: true},
	"PUT /v1/fleet/playbooks/{playbookID}":             {AdminBypass: true},
	"DELETE /v1/fleet/playbooks/{playbookID}":          {AdminBypass: true},
	"POST /v1/fleet/playbooks/{playbookID}/activate":   {AdminBypass: true},

	// Playbook run tracking (recording called by gateway service account; reads open to any authenticated user)
	"POST /v1/fleet/playbooks/{playbookID}/runs": {ServiceOnly: true, AdminBypass: true},
	"GET /v1/fleet/playbooks/{playbookID}/runs":  {AdminBypass: true},
	"GET /v1/fleet/playbooks/{playbookID}/stats": {AdminBypass: true},
	"PATCH /v1/fleet/playbook-runs/{runID}":      {AdminBypass: true},
	"GET /v1/fleet/playbook-runs/{runID}":        {AdminBypass: true},

	// Upload endpoints (operator file uploads, e.g. PostgreSQL log files)
	"POST /v1/uploads":                     {AdminBypass: true},
	"GET /v1/uploads/{uploadID}":           {AdminBypass: true},
	"GET /v1/uploads/{uploadID}/content":   {AdminBypass: true},

	// Tool result endpoints
	"POST /v1/tool-results": {ServiceOnly: true, AdminBypass: true},
	"GET /v1/tool-results":  {AdminBypass: true},

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

	// ── Rollback & Undo ───────────────────────────────────────────────────────

	// Read-only: any authenticated caller can query rollbacks and derive plans.
	"GET /v1/rollbacks":                          {AdminBypass: true},
	"GET /v1/rollbacks/{rollbackID}":             {AdminBypass: true},
	"POST /v1/events/{eventID}/rollback-plan":    {AdminBypass: true},
	"GET /v1/fleet/jobs/{jobID}/rollback":        {AdminBypass: true},

	// Mutation: requires operator or admin role.
	"POST /v1/rollbacks": {
		RequireRoles: []string{"operator", "admin"},
		AdminBypass:  true,
	},
	"POST /v1/rollbacks/{rollbackID}/cancel": {
		RequireRoles: []string{"operator", "admin"},
		AdminBypass:  true,
	},
	"POST /v1/fleet/jobs/{jobID}/rollback": {
		RequireRoles: []string{"operator", "fleet-approver", "admin"},
		AdminBypass:  true,
	},
}
