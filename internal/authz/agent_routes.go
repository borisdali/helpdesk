package authz

// DefaultAgentPermissions is the single source of truth for authorization on
// agent servers. Keys are Go 1.22 ServeMux patterns exactly as registered in
// agentutil.registerDirectToolRoutes.
//
// The /tool/{name} endpoint is the only agent-side route that requires
// protection — it bypasses the LLM layer and executes tool implementations
// directly. All other agent routes (A2A /invoke, /.well-known/agent-card.json)
// use the A2A protocol's own transport security and are not covered here.
var DefaultAgentPermissions = map[string]Permission{
	// POST /tool/{name}: service accounts only.
	// Called by the gateway (dispatchDirectTool) on behalf of an authenticated
	// user. Human callers are never permitted to invoke this endpoint directly
	// — they must go through the gateway, which enforces its own authz, audits
	// the call, and propagates the verified principal in the request body.
	"POST /tool/{name}": {ServiceOnly: true, AdminBypass: true},
}
