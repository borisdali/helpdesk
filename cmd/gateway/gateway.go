package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/identity"
	"helpdesk/internal/infra"
	"helpdesk/internal/toolregistry"
)

// agentNameDB is the expected name for the database agent.
const agentNameDB = "postgres_database_agent"

// agentNameK8s is the expected name for the k8s agent.
const agentNameK8s = "k8s_agent"

// agentNameIncident is the expected name for the incident agent.
const agentNameIncident = "incident_agent"

// agentNameResearch is the expected name for the research agent.
const agentNameResearch = "research_agent"

// Gateway translates REST requests into A2A calls to sub-agents.
type Gateway struct {
	agents           map[string]*discovery.Agent
	clients          map[string]*a2aclient.Client
	infra            *infra.Config
	auditor          *audit.GatewayAuditor
	auditURL         string                  // URL to auditd service for governance queries
	identityProvider identity.Provider       // resolves caller identity on every request
	operatingMode    string                  // "readonly" or "fix"
	toolRegistry     *toolregistry.Registry  // catalog of discovered tools
	plannerLLM       func(ctx context.Context, prompt string) (string, error) // injectable for tests
}

// NewGateway creates a Gateway and establishes A2A clients for each agent.
func NewGateway(agents map[string]*discovery.Agent) *Gateway {
	clients := make(map[string]*a2aclient.Client, len(agents))
	for name, agent := range agents {
		client, err := a2aclient.NewFromCard(context.Background(), agent.Card)
		if err != nil {
			slog.Warn("failed to create A2A client", "agent", name, "err", err)
			continue
		}
		clients[name] = client
		slog.Info("A2A client ready", "agent", name)
	}
	return &Gateway{agents: agents, clients: clients, plannerLLM: callPlannerLLM}
}

// SetInfraConfig sets the infrastructure configuration for inventory queries.
func (g *Gateway) SetInfraConfig(config *infra.Config) {
	g.infra = config
}

// SetAuditor sets the audit logger for the gateway.
func (g *Gateway) SetAuditor(auditor *audit.GatewayAuditor) {
	g.auditor = auditor
}

// SetAuditURL sets the auditd service URL for governance queries.
func (g *Gateway) SetAuditURL(url string) {
	g.auditURL = url
}

// SetIdentityProvider sets the identity provider used to resolve caller identity.
func (g *Gateway) SetIdentityProvider(p identity.Provider) {
	g.identityProvider = p
}

// SetOperatingMode sets the operating mode.
func (g *Gateway) SetOperatingMode(mode string) {
	g.operatingMode = mode
}

// SetToolRegistry sets the tool registry for direct tool call validation.
func (g *Gateway) SetToolRegistry(r *toolregistry.Registry) {
	g.toolRegistry = r
}

// resolveRequest extracts the verified principal and declared purpose from an
// HTTP request. Falls back to NoAuthProvider behaviour when no provider is set.
// The returned bool is true only when the caller explicitly declared a purpose.
func (g *Gateway) resolveRequest(r *http.Request, purposeFromBody, purposeNoteFromBody string) (identity.ResolvedPrincipal, string, string, bool, error) {
	var principal identity.ResolvedPrincipal
	if g.identityProvider != nil {
		var err error
		principal, err = g.identityProvider.Resolve(r)
		if err != nil {
			slog.Warn("gateway: identity resolution failed", "err", err)
			return identity.ResolvedPrincipal{}, "", "", false, err
		}
	} else {
		// No provider configured — legacy no-auth behaviour.
		principal = identity.ResolvedPrincipal{
			UserID:     r.Header.Get("X-User"),
			AuthMethod: "header",
		}
	}

	purpose, purposeExplicit := identity.PurposeFromRequest(
		r.Header.Get("X-Purpose"),
		purposeFromBody,
	)
	purposeNote := r.Header.Get("X-Purpose-Note")
	if purposeNote == "" {
		purposeNote = purposeNoteFromBody
	}
	return principal, purpose, purposeNote, purposeExplicit, nil
}

// agentAliases maps short names (used in the /query endpoint) to internal
// agent names used for client lookup.
var agentAliases = map[string]string{
	"database": agentNameDB,
	"db":       agentNameDB,
	"k8s":      agentNameK8s,
	"incident": agentNameIncident,
	"research": agentNameResearch,
}

// RegisterRoutes sets up the REST endpoint handlers.
func (g *Gateway) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}` + "\n")) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/agents", g.handleListAgents)
	mux.HandleFunc("GET /api/v1/tools", g.handleListTools)
	mux.HandleFunc("GET /api/v1/tools/{toolName}", g.handleGetTool)
	mux.HandleFunc("POST /api/v1/query", g.handleQuery)
	mux.HandleFunc("POST /api/v1/incidents", g.handleCreateIncident)
	mux.HandleFunc("GET /api/v1/incidents", g.handleListIncidents)
	mux.HandleFunc("POST /api/v1/db/{tool}", g.handleDBTool)
	mux.HandleFunc("POST /api/v1/k8s/{tool}", g.handleK8sTool)
	mux.HandleFunc("POST /api/v1/research", g.handleResearch)
	mux.HandleFunc("GET /api/v1/infrastructure", g.handleListInfrastructure)
	mux.HandleFunc("GET /api/v1/databases", g.handleListDatabases)
	mux.HandleFunc("GET /api/v1/governance", g.handleGovernance)
	mux.HandleFunc("GET /api/v1/governance/policies", g.handleGovernancePolicies)
	mux.HandleFunc("GET /api/v1/governance/explain", g.handleGovernanceExplain)
	mux.HandleFunc("GET /api/v1/governance/events", g.handleGovernanceEvents)
	mux.HandleFunc("GET /api/v1/governance/events/{eventID}", g.handleGovernanceEvent)
	mux.HandleFunc("GET /api/v1/governance/approvals/pending", g.handleGovernanceApprovalsPending)
	mux.HandleFunc("GET /api/v1/governance/approvals", g.handleGovernanceApprovals)
	mux.HandleFunc("GET /api/v1/governance/verify", g.handleGovernanceVerify)
	mux.HandleFunc("GET /api/v1/governance/journeys", g.handleGovernanceJourneys)
	mux.HandleFunc("GET /api/v1/governance/govbot/runs", g.handleGovernanceGovbotRuns)

	// Fleet job planner
	mux.HandleFunc("POST /api/v1/fleet/plan", g.handleFleetPlan)

	// Fleet runner job visibility endpoints
	mux.HandleFunc("POST /api/v1/fleet/jobs", g.handleFleetCreateJob)
	mux.HandleFunc("GET /api/v1/fleet/jobs", g.handleFleetListJobs)
	mux.HandleFunc("GET /api/v1/fleet/jobs/{jobID}", g.handleFleetGetJob)
	mux.HandleFunc("GET /api/v1/fleet/jobs/{jobID}/servers", g.handleFleetGetJobServers)
	mux.HandleFunc("GET /api/v1/fleet/jobs/{jobID}/servers/{serverName}/steps", g.handleFleetGetServerSteps)
	mux.HandleFunc("GET /api/v1/fleet/jobs/{jobID}/approval/{approvalID}", g.handleFleetGetJobApproval)
}

// --- Handlers ---

func (g *Gateway) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent       string `json:"agent"`
		Message     string `json:"message"`
		Query       string `json:"query"`        // alias for message; both are accepted
		User        string `json:"user"`         // caller identity recorded in the audit trail
		Purpose     string `json:"purpose"`      // why this request is being made
		PurposeNote string `json:"purpose_note"` // optional free-text (e.g. incident number)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Message == "" {
		req.Message = req.Query
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, `"message" (or "query") is required`)
		return
	}

	agentName, ok := agentAliases[req.Agent]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown agent %q (valid: database, db, k8s, incident, research)", req.Agent))
		return
	}

	// Bridge JSON body "user" into the canonical X-User header so that
	// proxyToAgentWithTool can read a single source of truth.
	// The header takes precedence if both are supplied.
	if req.User != "" && r.Header.Get("X-User") == "" {
		r.Header.Set("X-User", req.User)
	}
	// Bridge purpose fields into headers so proxyToAgentWithTool has one place to read them.
	if req.Purpose != "" && r.Header.Get("X-Purpose") == "" {
		r.Header.Set("X-Purpose", req.Purpose)
	}
	if req.PurposeNote != "" && r.Header.Get("X-Purpose-Note") == "" {
		r.Header.Set("X-Purpose-Note", req.PurposeNote)
	}

	g.proxyToAgent(w, r, agentName, req.Message)
}

func (g *Gateway) handleListTools(w http.ResponseWriter, r *http.Request) {
	if g.toolRegistry == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"tools":   []any{},
			"message": "Tool registry not available.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": g.toolRegistry.List(),
		"count": len(g.toolRegistry.List()),
	})
}

func (g *Gateway) handleGetTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("toolName")
	if g.toolRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "tool registry not available")
		return
	}
	entry, ok := g.toolRegistry.Get(toolName)
	if !ok {
		writeError(w, http.StatusNotFound, "tool not found: "+toolName)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (g *Gateway) handleListAgents(w http.ResponseWriter, r *http.Request) {
	type agentInfo struct {
		Name        string          `json:"name"`
		InvokeURL   string          `json:"invoke_url"`
		Description string          `json:"description,omitempty"`
		Version     string          `json:"version,omitempty"`
		Skills      []a2a.AgentSkill `json:"skills,omitempty"`
	}

	var agents []agentInfo
	for _, agent := range g.agents {
		info := agentInfo{
			Name:      agent.Name,
			InvokeURL: agent.InvokeURL,
		}
		if agent.Card != nil {
			info.Description = agent.Card.Description
			info.Version = agent.Card.Version
			info.Skills = agent.Card.Skills
		}
		agents = append(agents, info)
	}
	writeJSON(w, http.StatusOK, agents)
}

func (g *Gateway) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	prompt := buildToolPrompt("create_incident_bundle", args)
	g.proxyToAgent(w, r, agentNameIncident, prompt)
}

func (g *Gateway) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	g.proxyToAgent(w, r, agentNameIncident, "List all previously created incident bundles.")
}

func (g *Gateway) handleDBTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	// Validate tool exists in registry.
	if g.toolRegistry != nil {
		if _, ok := g.toolRegistry.Get(toolName); !ok {
			writeError(w, http.StatusBadRequest, "unknown tool: "+toolName)
			return
		}
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	g.dispatchDirectTool(w, r, agentNameDB, toolName, args)
}

func (g *Gateway) handleK8sTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	// Validate tool exists in registry.
	if g.toolRegistry != nil {
		if _, ok := g.toolRegistry.Get(toolName); !ok {
			writeError(w, http.StatusBadRequest, "unknown tool: "+toolName)
			return
		}
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	g.dispatchDirectTool(w, r, agentNameK8s, toolName, args)
}

func (g *Gateway) handleResearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	g.proxyToAgent(w, r, agentNameResearch, req.Query)
}

func (g *Gateway) handleListInfrastructure(w http.ResponseWriter, r *http.Request) {
	if g.infra == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"message":    "No infrastructure configuration loaded. Set HELPDESK_INFRA_CONFIG.",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   true,
		"db_servers":   len(g.infra.DBServers),
		"k8s_clusters": len(g.infra.K8sClusters),
		"vms":          len(g.infra.VMs),
		"databases":    g.infra.ListDatabases(),
		"summary":      g.infra.Summary(),
	})
}

func (g *Gateway) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	if g.infra == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"databases": []any{},
			"message":   "No infrastructure configuration loaded.",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"databases": g.infra.ListDatabases(),
		"count":     len(g.infra.DBServers),
	})
}

func (g *Gateway) handleGovernance(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/governance/info")
}

func (g *Gateway) handleGovernancePolicies(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/governance/policies")
}

func (g *Gateway) handleGovernanceEvents(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/events")
}

func (g *Gateway) handleGovernanceApprovals(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/approvals")
}

func (g *Gateway) handleGovernanceApprovalsPending(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/approvals/pending")
}

func (g *Gateway) handleGovernanceVerify(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/verify")
}

func (g *Gateway) handleGovernanceExplain(w http.ResponseWriter, r *http.Request) {
	// Resolve the caller's identity and inject it as query parameters so the
	// explain endpoint can evaluate service-account and user-specific policies.
	// The caller may have already supplied user_id/service/role explicitly —
	// only inject when the field is absent, so explicit overrides are preserved.
	principal, purpose, _, _, err := g.resolveRequest(r, "", "")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "identity resolution failed: "+err.Error())
		return
	}

	q := r.URL.Query()
	if q.Get("user_id") == "" && principal.UserID != "" {
		q.Set("user_id", principal.UserID)
	}
	if q.Get("service") == "" && principal.Service != "" {
		q.Set("service", principal.Service)
	}
	if q.Get("role") == "" && len(principal.Roles) > 0 {
		q.Set("role", principal.Roles[0])
	}
	if q.Get("purpose") == "" && purpose != "" {
		q.Set("purpose", purpose)
	}

	// Rebuild the request URL with the enriched query string.
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	g.proxyGovernanceRequest(w, r2, "/v1/governance/explain")
}

func (g *Gateway) handleGovernanceEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventID")
	g.proxyGovernanceRequest(w, r, "/v1/events/"+eventID)
}

func (g *Gateway) handleGovernanceJourneys(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/journeys")
}

func (g *Gateway) handleGovernanceGovbotRuns(w http.ResponseWriter, r *http.Request) {
	g.proxyGovernanceRequest(w, r, "/v1/govbot/runs")
}

func (g *Gateway) handleFleetCreateJob(w http.ResponseWriter, r *http.Request) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "fleet service not configured. Set HELPDESK_AUDIT_URL to enable.")
		return
	}

	// Read the body so we can inject submitted_by.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	// Inject submitted_by from resolved identity.
	principal, purpose, _, _, err := g.resolveRequest(r, "", "")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication failed: "+err.Error())
		return
	}
	if _, hasSubmittedBy := body["submitted_by"]; !hasSubmittedBy {
		body["submitted_by"] = principal.EffectiveID()
	}

	modified, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode body")
		return
	}

	// Forward to auditd and intercept the response to extract job_id.
	// We need the job_id to record an audit anchor event that makes this
	// fleet job visible as a journey in GET /v1/journeys.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		g.auditURL+"/v1/fleet/jobs", strings.NewReader(string(modified)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("fleet service unavailable", "err", err)
		writeError(w, http.StatusBadGateway, "fleet service unavailable")
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")

	// On success, record a gateway_request anchor event so the fleet job
	// appears as a single journey. The trace ID tr_<jobID> is shared
	// by all subsequent tool calls for this job.
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		var created struct {
			JobID string `json:"job_id"`
			Name  string `json:"name"`
		}
		if jsonErr := json.Unmarshal(respBytes, &created); jsonErr == nil && created.JobID != "" {
			traceID := "tr_" + created.JobID
			w.Header().Set("X-Trace-ID", traceID)

			jobName := created.Name
			if jobName == "" {
				if n, ok := body["name"].(string); ok {
					jobName = n
				}
			}
			g.recordAudit(r.Context(), &audit.GatewayRequest{
				TraceID:           traceID,
				Endpoint:          r.URL.Path,
				Method:            r.Method,
				Message:           "fleet job: " + jobName,
				StartTime:         time.Now(),
				Status:            "success",
				HTTPCode:          resp.StatusCode,
				Principal:         principal.EffectiveID(),
				ResolvedPrincipal: principal,
				Purpose:           purpose,
			})
		}
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(respBytes) //nolint:errcheck
}

func (g *Gateway) handleFleetListJobs(w http.ResponseWriter, r *http.Request) {
	g.proxyFleetRequest(w, r, "/v1/fleet/jobs", r.Method, nil)
}

func (g *Gateway) handleFleetGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	g.proxyFleetRequest(w, r, "/v1/fleet/jobs/"+jobID, r.Method, nil)
}

func (g *Gateway) handleFleetGetJobServers(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	g.proxyFleetRequest(w, r, "/v1/fleet/jobs/"+jobID+"/servers", r.Method, nil)
}

func (g *Gateway) handleFleetGetServerSteps(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	serverName := r.PathValue("serverName")
	g.proxyFleetRequest(w, r, "/v1/fleet/jobs/"+jobID+"/servers/"+serverName+"/steps", r.Method, nil)
}

func (g *Gateway) handleFleetGetJobApproval(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	approvalID := r.PathValue("approvalID")
	g.proxyFleetRequest(w, r, "/v1/fleet/jobs/"+jobID+"/approval/"+approvalID, r.Method, nil)
}

// proxyFleetRequest forwards a fleet request to the auditd service, preserving method and body.
func (g *Gateway) proxyFleetRequest(w http.ResponseWriter, r *http.Request, path, method string, body []byte) {
	if g.auditURL == "" {
		writeError(w, http.StatusServiceUnavailable, "fleet service not configured. Set HELPDESK_AUDIT_URL to enable.")
		return
	}

	targetURL := g.auditURL + path
	if q := r.URL.RawQuery; q != "" {
		targetURL += "?" + q
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}

	req, err := http.NewRequestWithContext(r.Context(), method, targetURL, bodyReader)
	if err != nil {
		slog.Error("failed to create fleet proxy request", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("failed to proxy fleet request", "err", err)
		writeError(w, http.StatusBadGateway, "fleet service unavailable")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// proxyGovernanceRequest forwards a request to the auditd service, preserving query parameters.
func (g *Gateway) proxyGovernanceRequest(w http.ResponseWriter, r *http.Request, path string) {
	if g.auditURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "Governance service not configured. Set HELPDESK_AUDIT_URL to enable.",
		})
		return
	}

	targetURL := g.auditURL + path
	if q := r.URL.RawQuery; q != "" {
		targetURL += "?" + q
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		slog.Error("failed to query governance service", "err", err)
		writeError(w, http.StatusBadGateway, "governance service unavailable")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- A2A call ---

// proxyToAgent sends a text message to an agent and returns the response.
func (g *Gateway) proxyToAgent(w http.ResponseWriter, r *http.Request, agentName, prompt string) {
	g.proxyToAgentWithTool(w, r, agentName, "", nil, prompt)
}

// proxyToAgentWithTool sends a text message to an agent with tool name for classification.
func (g *Gateway) proxyToAgentWithTool(w http.ResponseWriter, r *http.Request, agentName, toolName string, toolParams map[string]any, prompt string) {
	start := time.Now()
	requestID := uuid.New().String()[:8]

	// Generate or extract trace ID for end-to-end correlation.
	// Use a prefix that encodes the call origin so audit queries can distinguish:
	//   tr_ — natural-language query (POST /api/v1/query)
	//   dt_ — direct tool invocation (POST /api/v1/db/{tool}, /api/v1/k8s/{tool})
	traceID := r.Header.Get("X-Trace-ID")
	if traceID == "" {
		if toolName != "" {
			traceID = audit.NewTraceIDWithPrefix("dt_")
		} else {
			traceID = audit.NewTraceID() // "tr_"
		}
	}
	// Set the trace ID on the response immediately so it is present on all
	// responses, including early-return error paths (401, 502, etc.).
	w.Header().Set("X-Trace-ID", traceID)

	// Resolve caller identity and purpose. Purpose fields may arrive via headers
	// (set directly or bridged from the JSON body in handleQuery).
	resolvedPrincipal, purpose, purposeNote, purposeExplicit, err := g.resolveRequest(r, "", "")
	if err != nil {
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID: requestID,
			TraceID:   traceID,
			Endpoint:  r.URL.Path,
			Method:    r.Method,
			Agent:     agentName,
			ToolName:  toolName,
			Message:   prompt,
			StartTime: start,
			Duration:  time.Since(start),
			Status:    "error",
			Error:     "authentication failed: " + err.Error(),
			HTTPCode:  http.StatusUnauthorized,
		})
		writeError(w, http.StatusUnauthorized, "authentication failed: "+err.Error())
		return
	}
	principalStr := resolvedPrincipal.EffectiveID()

	client, ok := g.clients[agentName]
	if !ok {
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    toolParams,
			Message:           prompt,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             "agent not available",
			HTTPCode:          http.StatusBadGateway,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("agent %q not available", agentName))
		return
	}

	slog.Info("gateway: proxying request", "agent", agentName, "prompt_len", len(prompt),
		"principal", principalStr, "purpose", purpose)

	// Build A2A metadata: trace_id plus the full principal and purpose so that
	// downstream agents can enforce policy on behalf of the original caller.
	meta := map[string]any{"trace_id": traceID}
	if resolvedPrincipal.UserID != "" {
		meta["user_id"] = resolvedPrincipal.UserID
	}
	if len(resolvedPrincipal.Roles) > 0 {
		meta["roles"] = resolvedPrincipal.Roles
	}
	if resolvedPrincipal.Service != "" {
		meta["service"] = resolvedPrincipal.Service
	}
	if resolvedPrincipal.AuthMethod != "" {
		meta["auth_method"] = resolvedPrincipal.AuthMethod
	}
	if purpose != "" {
		meta["purpose"] = purpose
	}
	if purposeNote != "" {
		meta["purpose_note"] = purposeNote
	}
	meta["purpose_explicit"] = purposeExplicit

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: prompt})
	msg.Metadata = meta
	result, err := client.SendMessage(r.Context(), &a2a.MessageSendParams{Message: msg})
	if err != nil {
		slog.Error("gateway: A2A call failed", "agent", agentName, "err", err)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    toolParams,
			Message:           prompt,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             err.Error(),
			HTTPCode:          http.StatusBadGateway,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("A2A call to %s failed: %v", agentName, err))
		return
	}

	response := extractResponse(result)
	response.AgentName = agentName

	// If the A2A task itself failed (runner-level failure), return 502.
	if response.State == string(a2a.TaskStateFailed) {
		slog.Error("gateway: A2A task failed", "agent", agentName, "task_id", response.TaskID, "text", response.Text)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    toolParams,
			Message:           prompt,
			Response:          response.Text,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             "agent task failed: " + response.Text,
			HTTPCode:          http.StatusBadGateway,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusBadGateway, "agent task failed: "+response.Text)
		return
	}

	// For direct tool calls, detect policy denial surfaced in the agent response.
	// policy.DeniedError always produces "policy denied: ..." text, which the ADK
	// framework feeds verbatim as the FunctionResponse error back to the LLM.
	if toolName != "" && isPolicyDenial(response.Text) {
		slog.Warn("gateway: policy denied", "agent", agentName, "tool", toolName, "trace_id", traceID)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    toolParams,
			Message:           prompt,
			Response:          response.Text,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "denied",
			Error:             "policy denied",
			HTTPCode:          http.StatusForbidden,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusForbidden, response.Text)
		return
	}

	// For direct tool calls, detect tool-level execution failures that the agent
	// surfaces as text output (not as Go errors or A2A task failures).
	// The database agent's errorResult() always produces "---\nERROR — <tool> failed"
	// so programmatic callers (fleet-runner) receive 422 instead of a false 200.
	// NL queries (toolName == "") are excluded — the LLM response is always shown as-is.
	if toolName != "" && isToolError(response.Text) {
		slog.Warn("gateway: tool execution failed", "agent", agentName, "tool", toolName, "trace_id", traceID)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    toolParams,
			Message:           prompt,
			Response:          response.Text,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             "tool execution failed",
			HTTPCode:          http.StatusUnprocessableEntity,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusUnprocessableEntity, response.Text)
		return
	}

	// Record successful request with response
	g.recordAudit(r.Context(), &audit.GatewayRequest{
		RequestID:         requestID,
		TraceID:           traceID,
		Endpoint:          r.URL.Path,
		Method:            r.Method,
		Agent:             agentName,
		ToolName:          toolName,
		ToolParameters:    toolParams,
		Message:           prompt,
		Response:          response.Text,
		StartTime:         start,
		Duration:          time.Since(start),
		Status:            "success",
		HTTPCode:          http.StatusOK,
		Principal:         principalStr,
		ResolvedPrincipal: resolvedPrincipal,
		Purpose:           purpose,
		PurposeNote:       purposeNote,
	})

	writeJSON(w, http.StatusOK, response)
}

// recordAudit sends a request to the auditor if configured.
func (g *Gateway) recordAudit(ctx context.Context, req *audit.GatewayRequest) {
	if g.auditor == nil {
		return
	}
	if err := g.auditor.RecordRequest(ctx, req); err != nil {
		slog.Warn("failed to record audit", "error", err)
	}
}

// directToolReq is the JSON body sent to POST /tool/{name} on an agent.
type directToolReq struct {
	TraceID         string                     `json:"trace_id,omitempty"`
	Principal       identity.ResolvedPrincipal `json:"principal,omitempty"`
	Purpose         string                     `json:"purpose,omitempty"`
	PurposeNote     string                     `json:"purpose_note,omitempty"`
	PurposeExplicit bool                       `json:"purpose_explicit,omitempty"`
	Args            map[string]any             `json:"args"`
}

// directToolResp is the JSON body returned by POST /tool/{name}.
type directToolResp struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// dispatchDirectTool sends a structured tool call directly to the agent's
// /tool/{name} HTTP endpoint, bypassing the ADK/LLM layer entirely.
// This eliminates LLM narration and parameter misinterpretation for fleet jobs.
func (g *Gateway) dispatchDirectTool(w http.ResponseWriter, r *http.Request, agentName, toolName string, args map[string]any) {
	start := time.Now()
	requestID := uuid.New().String()[:8]

	// Generate or propagate trace ID.
	traceID := r.Header.Get("X-Trace-ID")
	if traceID == "" {
		traceID = audit.NewTraceIDWithPrefix("dt_")
	}
	w.Header().Set("X-Trace-ID", traceID)

	// Resolve caller identity and purpose.
	resolvedPrincipal, purpose, purposeNote, purposeExplicit, err := g.resolveRequest(r, "", "")
	if err != nil {
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID: requestID,
			TraceID:   traceID,
			Endpoint:  r.URL.Path,
			Method:    r.Method,
			Agent:     agentName,
			ToolName:  toolName,
			StartTime: start,
			Duration:  time.Since(start),
			Status:    "error",
			Error:     "authentication failed: " + err.Error(),
			HTTPCode:  http.StatusUnauthorized,
		})
		writeError(w, http.StatusUnauthorized, "authentication failed: "+err.Error())
		return
	}
	principalStr := resolvedPrincipal.EffectiveID()

	// Resolve agent base URL (strip /invoke suffix from InvokeURL).
	agentInfo, ok := g.agents[agentName]
	if !ok {
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    args,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             "agent not available",
			HTTPCode:          http.StatusBadGateway,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("agent %q not available", agentName))
		return
	}
	baseURL := strings.TrimSuffix(agentInfo.InvokeURL, "/invoke")

	slog.Info("gateway: direct tool dispatch", "agent", agentName, "tool", toolName,
		"principal", principalStr, "purpose", purpose)

	// Build request body carrying trace context + args.
	reqBody := directToolReq{
		TraceID:         traceID,
		Principal:       resolvedPrincipal,
		Purpose:         purpose,
		PurposeNote:     purposeNote,
		PurposeExplicit: purposeExplicit,
		Args:            args,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal tool request")
		return
	}

	// Call the agent's direct tool endpoint.
	toolURL := baseURL + "/tool/" + toolName
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, toolURL, bytes.NewReader(bodyBytes))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build tool request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	httpResp, err := client.Do(req)
	if err != nil {
		slog.Error("gateway: direct tool call failed", "agent", agentName, "tool", toolName, "err", err)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    args,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             err.Error(),
			HTTPCode:          http.StatusBadGateway,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("direct tool call to %s failed: %v", agentName, err))
		return
	}
	defer httpResp.Body.Close()
	respBytes, _ := io.ReadAll(httpResp.Body)

	var toolResp directToolResp
	if jsonErr := json.Unmarshal(respBytes, &toolResp); jsonErr != nil {
		// Treat unparseable response as raw text output.
		toolResp.Output = string(respBytes)
	}

	text := toolResp.Output
	if toolResp.Error != "" {
		text = toolResp.Error
	}

	// Map agent-level errors to appropriate HTTP status codes.
	if httpResp.StatusCode >= 400 || toolResp.Error != "" {
		httpStatus := httpResp.StatusCode
		if httpStatus < 400 {
			httpStatus = http.StatusUnprocessableEntity
		}

		auditStatus := "error"
		if isPolicyDenial(text) {
			auditStatus = "denied"
			httpStatus = http.StatusForbidden
		}

		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    args,
			Response:          text,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            auditStatus,
			Error:             text,
			HTTPCode:          httpStatus,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, httpStatus, text)
		return
	}

	// Success path — check for tool-level execution failures surfaced as text.
	if isToolError(text) {
		slog.Warn("gateway: tool execution failed", "agent", agentName, "tool", toolName, "trace_id", traceID)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:         requestID,
			TraceID:           traceID,
			Endpoint:          r.URL.Path,
			Method:            r.Method,
			Agent:             agentName,
			ToolName:          toolName,
			ToolParameters:    args,
			Response:          text,
			StartTime:         start,
			Duration:          time.Since(start),
			Status:            "error",
			Error:             "tool execution failed",
			HTTPCode:          http.StatusUnprocessableEntity,
			Principal:         principalStr,
			ResolvedPrincipal: resolvedPrincipal,
			Purpose:           purpose,
			PurposeNote:       purposeNote,
		})
		writeError(w, http.StatusUnprocessableEntity, text)
		return
	}

	g.recordAudit(r.Context(), &audit.GatewayRequest{
		RequestID:         requestID,
		TraceID:           traceID,
		Endpoint:          r.URL.Path,
		Method:            r.Method,
		Agent:             agentName,
		ToolName:          toolName,
		ToolParameters:    args,
		Response:          text,
		StartTime:         start,
		Duration:          time.Since(start),
		Status:            "success",
		HTTPCode:          http.StatusOK,
		Principal:         principalStr,
		ResolvedPrincipal: resolvedPrincipal,
		Purpose:           purpose,
		PurposeNote:       purposeNote,
	})

	// Return the same a2aResponse structure as the NL path for client compatibility.
	writeJSON(w, http.StatusOK, a2aResponse{
		AgentName: agentName,
		State:     "completed",
		Text:      text,
	})
}

// --- Response extraction ---

// a2aResponse is the structured response returned by the gateway.
type a2aResponse struct {
	AgentName string `json:"agent"`
	TaskID    string `json:"task_id,omitempty"`
	State     string `json:"state,omitempty"`
	Text      string `json:"text,omitempty"`
	Artifacts []any  `json:"artifacts,omitempty"`
}

// extractResponse pulls text and artifacts from a SendMessageResult.
func extractResponse(result a2a.SendMessageResult) a2aResponse {
	resp := a2aResponse{}

	switch v := result.(type) {
	case *a2a.Task:
		resp.TaskID = string(v.ID)
		resp.State = string(v.Status.State)

		// Extract text from status message.
		if v.Status.Message != nil {
			resp.Text = extractText(v.Status.Message.Parts)
		}

		// If no status text, try history (last agent message).
		if resp.Text == "" {
			for i := len(v.History) - 1; i >= 0; i-- {
				if v.History[i].Role == a2a.MessageRoleAgent {
					resp.Text = extractText(v.History[i].Parts)
					break
				}
			}
		}

		// Extract artifacts.
		for _, a := range v.Artifacts {
			resp.Artifacts = append(resp.Artifacts, map[string]any{
				"id":    a.ID,
				"name":  a.Name,
				"parts": extractText(a.Parts),
			})
		}

		// If still no text, use the first artifact's content.
		if resp.Text == "" && len(v.Artifacts) > 0 {
			resp.Text = extractText(v.Artifacts[0].Parts)
		}

	case *a2a.Message:
		resp.Text = extractText(v.Parts)
	}

	return resp
}

// extractText concatenates all text parts from a content parts slice.
func extractText(parts a2a.ContentParts) string {
	var texts []string
	for _, p := range parts {
		if tp, ok := p.(a2a.TextPart); ok {
			texts = append(texts, tp.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// isPolicyDenial reports whether the agent response text contains a policy denial.
// policy.DeniedError always produces "policy denied: ..." text, which the ADK framework
// feeds verbatim as the FunctionResponse error; the LLM typically reproduces it in its reply.
func isPolicyDenial(text string) bool {
	return strings.Contains(strings.ToLower(text), "policy denied")
}

// isToolError reports whether the agent response text signals a tool-level execution
// failure. The database agent deliberately returns errors as text output (rather than
// Go errors) using the errorResult() helper, which always produces the marker
// "---\nERROR — <tool> failed". We detect this here so fleet-runner and other
// programmatic callers receive a non-200 status instead of a false success.
func isToolError(text string) bool {
	return strings.Contains(text, "\nERROR —")
}

// --- Prompt construction ---

// buildToolPrompt constructs a clear instruction for the agent to call a specific tool.
func buildToolPrompt(toolName string, args map[string]any) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Call the %s tool", toolName))

	if len(args) > 0 {
		sb.WriteString(" with the following parameters: ")
		pairs := make([]string, 0, len(args))
		for k, v := range args {
			pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
		}
		sb.WriteString(strings.Join(pairs, ", "))
	}

	sb.WriteString(".")
	return sb.String()
}

// --- Utilities ---

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
