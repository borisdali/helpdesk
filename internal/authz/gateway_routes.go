package authz

// DefaultGatewayPermissions is the single source of truth for authorization on
// the REST gateway. Keys are Go 1.22 ServeMux patterns exactly as registered
// in Gateway.RegisterRoutes.
var DefaultGatewayPermissions = map[string]Permission{
	// ── Public: no authentication required ────────────────────────────────────
	"GET /health":                  {AllowAnonymous: true},
	"GET /api/v1/agents":           {AllowAnonymous: true},
	"GET /api/v1/tools":            {AllowAnonymous: true},
	"GET /api/v1/tools/{toolName}": {AllowAnonymous: true},
	"GET /api/v1/roles":            {AllowAnonymous: true},

	// ── Authenticated: any verified (non-anonymous) user ──────────────────────
	"POST /api/v1/query":         {AdminBypass: true},
	"POST /api/v1/incidents":     {AdminBypass: true},
	"GET /api/v1/incidents":      {AdminBypass: true},
	"POST /api/v1/research":      {AdminBypass: true},
	"GET /api/v1/infrastructure": {AdminBypass: true},
	"GET /api/v1/databases":      {AdminBypass: true},

	// Governance reads
	"GET /api/v1/governance":                   {AdminBypass: true},
	"GET /api/v1/governance/policies":          {AdminBypass: true},
	"GET /api/v1/governance/explain":           {AdminBypass: true},
	"GET /api/v1/governance/events":            {AdminBypass: true},
	"GET /api/v1/governance/events/{eventID}":  {AdminBypass: true},
	"GET /api/v1/governance/approvals/pending": {AdminBypass: true},
	"GET /api/v1/governance/approvals":         {AdminBypass: true},
	"GET /api/v1/governance/verify":            {AdminBypass: true},
	"GET /api/v1/governance/journeys":          {AdminBypass: true},
	"GET /api/v1/governance/govbot/runs":       {AdminBypass: true},

	// Fleet reads and plan (plan is a dry-run — any authenticated user may preview)
	"POST /api/v1/fleet/plan":                                   {AdminBypass: true},
	"POST /api/v1/fleet/snapshot":                               {AdminBypass: true},
	"POST /api/v1/fleet/review":                                 {AdminBypass: true},
	"GET /api/v1/fleet/jobs":                                    {AdminBypass: true},
	"GET /api/v1/fleet/jobs/{jobID}":                            {AdminBypass: true},
	"GET /api/v1/fleet/jobs/{jobID}/servers":                    {AdminBypass: true},
	"GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}":       {AdminBypass: true},
	"GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}/steps": {AdminBypass: true},
	"GET /api/v1/fleet/jobs/{jobID}/approval/{approvalID}":      {AdminBypass: true},

	// Fleet playbooks: CRUD + run (any authenticated user; fleet-operator to create/delete)
	"GET /api/v1/fleet/playbooks":                          {AdminBypass: true},
	"GET /api/v1/fleet/playbooks/{playbookID}":             {AdminBypass: true},
	"POST /api/v1/fleet/playbooks/{playbookID}/run":        {AdminBypass: true},
	"POST /api/v1/fleet/playbooks": {
		RequireRoles: []string{"fleet-operator"},
		AdminBypass:  true,
	},
	"DELETE /api/v1/fleet/playbooks/{playbookID}": {
		RequireRoles: []string{"fleet-operator"},
		AdminBypass:  true,
	},

	// ── Role-required ─────────────────────────────────────────────────────────

	// DB tools: direct tool invocation bypasses the orchestrator; restrict to
	// roles with a legitimate operational need.
	"POST /api/v1/db/{tool}": {
		RequireRoles: []string{"dba", "sre", "oncall", "sre-automation"},
		AdminBypass:  true,
	},

	// K8s tools: same rationale.
	"POST /api/v1/k8s/{tool}": {
		RequireRoles: []string{"sre", "k8s-admin", "oncall", "sre-automation"},
		AdminBypass:  true,
	},

	// Fleet job submission: fleet-operator role required to create a live job.
	"POST /api/v1/fleet/jobs": {
		RequireRoles: []string{"fleet-operator"},
		AdminBypass:  true,
	},
}
