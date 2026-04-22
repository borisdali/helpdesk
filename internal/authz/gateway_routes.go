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

	// Tool result query
	"GET /api/v1/tool-results": {AdminBypass: true},

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

	// Uploads: operator file uploads for agent analysis (e.g. PostgreSQL log files)
	"POST /api/v1/fleet/uploads":                       {AdminBypass: true},
	"GET /api/v1/fleet/uploads/{uploadID}":             {AdminBypass: true},
	"GET /api/v1/fleet/uploads/{uploadID}/content":     {AdminBypass: true},

	// Fleet playbooks: CRUD + run (any authenticated user; same as /fleet/plan)
	"GET /api/v1/fleet/playbooks":                      {AdminBypass: true},
	"GET /api/v1/fleet/playbooks/{playbookID}":         {AdminBypass: true},
	"POST /api/v1/fleet/playbooks":                     {AdminBypass: true},
	"PUT /api/v1/fleet/playbooks/{playbookID}":         {AdminBypass: true},
	"DELETE /api/v1/fleet/playbooks/{playbookID}":      {AdminBypass: true},
	"POST /api/v1/fleet/playbooks/from-trace":              {AdminBypass: true},
	"POST /api/v1/fleet/playbooks/import":                  {AdminBypass: true},
	"POST /api/v1/fleet/playbooks/{playbookID}/activate":   {AdminBypass: true},
	"POST /api/v1/fleet/playbooks/{playbookID}/run":        {AdminBypass: true},
	"GET /api/v1/fleet/playbooks/{playbookID}/runs":        {AdminBypass: true},
	"GET /api/v1/fleet/playbooks/{playbookID}/stats":       {AdminBypass: true},
	"PATCH /api/v1/fleet/playbook-runs/{runID}":        {AdminBypass: true},
	"GET /api/v1/fleet/playbook-runs/{runID}":          {AdminBypass: true},

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
