package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/google/uuid"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/infra"
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
	agents  map[string]*discovery.Agent
	clients map[string]*a2aclient.Client
	infra   *infra.Config
	auditor *audit.GatewayAuditor
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
	return &Gateway{agents: agents, clients: clients}
}

// SetInfraConfig sets the infrastructure configuration for inventory queries.
func (g *Gateway) SetInfraConfig(config *infra.Config) {
	g.infra = config
}

// SetAuditor sets the audit logger for the gateway.
func (g *Gateway) SetAuditor(auditor *audit.GatewayAuditor) {
	g.auditor = auditor
}

// agentAliases maps short names (used in the /query endpoint) to internal
// agent names used for client lookup.
var agentAliases = map[string]string{
	"database": agentNameDB,
	"db":       agentNameDB,
	"k8s":      agentNameK8s,
	"incident": agentNameIncident,
}

// RegisterRoutes sets up the REST endpoint handlers.
func (g *Gateway) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents", g.handleListAgents)
	mux.HandleFunc("POST /api/v1/query", g.handleQuery)
	mux.HandleFunc("POST /api/v1/incidents", g.handleCreateIncident)
	mux.HandleFunc("GET /api/v1/incidents", g.handleListIncidents)
	mux.HandleFunc("POST /api/v1/db/{tool}", g.handleDBTool)
	mux.HandleFunc("POST /api/v1/k8s/{tool}", g.handleK8sTool)
	mux.HandleFunc("POST /api/v1/research", g.handleResearch)
	mux.HandleFunc("GET /api/v1/infrastructure", g.handleListInfrastructure)
	mux.HandleFunc("GET /api/v1/databases", g.handleListDatabases)
}

// --- Handlers ---

func (g *Gateway) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent   string `json:"agent"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	agentName, ok := agentAliases[req.Agent]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown agent %q (valid: database, db, k8s, incident)", req.Agent))
		return
	}

	g.proxyToAgent(w, r, agentName, req.Message)
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
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	prompt := buildToolPrompt(toolName, args)
	g.proxyToAgentWithTool(w, r, agentNameDB, toolName, args, prompt)
}

func (g *Gateway) handleK8sTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	prompt := buildToolPrompt(toolName, args)
	g.proxyToAgentWithTool(w, r, agentNameK8s, toolName, args, prompt)
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

// --- A2A call ---

// proxyToAgent sends a text message to an agent and returns the response.
func (g *Gateway) proxyToAgent(w http.ResponseWriter, r *http.Request, agentName, prompt string) {
	g.proxyToAgentWithTool(w, r, agentName, "", nil, prompt)
}

// proxyToAgentWithTool sends a text message to an agent with tool name for classification.
func (g *Gateway) proxyToAgentWithTool(w http.ResponseWriter, r *http.Request, agentName, toolName string, toolParams map[string]any, prompt string) {
	start := time.Now()
	requestID := uuid.New().String()[:8]

	// Generate or extract trace ID for end-to-end correlation
	traceID := r.Header.Get("X-Trace-ID")
	if traceID == "" {
		traceID = audit.NewTraceID()
	}

	client, ok := g.clients[agentName]
	if !ok {
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:      requestID,
			TraceID:        traceID,
			Endpoint:       r.URL.Path,
			Method:         r.Method,
			Agent:          agentName,
			ToolName:       toolName,
			ToolParameters: toolParams,
			Message:        prompt,
			StartTime:      start,
			Duration:       time.Since(start),
			Status:         "error",
			Error:          "agent not available",
			HTTPCode:       http.StatusBadGateway,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("agent %q not available", agentName))
		return
	}

	slog.Info("gateway: proxying request", "agent", agentName, "prompt_len", len(prompt))

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: prompt})
	result, err := client.SendMessage(r.Context(), &a2a.MessageSendParams{Message: msg})
	if err != nil {
		slog.Error("gateway: A2A call failed", "agent", agentName, "err", err)
		g.recordAudit(r.Context(), &audit.GatewayRequest{
			RequestID:      requestID,
			TraceID:        traceID,
			Endpoint:       r.URL.Path,
			Method:         r.Method,
			Agent:          agentName,
			ToolName:       toolName,
			ToolParameters: toolParams,
			Message:        prompt,
			StartTime:      start,
			Duration:       time.Since(start),
			Status:         "error",
			Error:          err.Error(),
			HTTPCode:       http.StatusBadGateway,
		})
		writeError(w, http.StatusBadGateway, fmt.Sprintf("A2A call to %s failed: %v", agentName, err))
		return
	}

	response := extractResponse(result)
	response.AgentName = agentName

	// Record successful request with response
	g.recordAudit(r.Context(), &audit.GatewayRequest{
		RequestID:      requestID,
		TraceID:        traceID,
		Endpoint:       r.URL.Path,
		Method:         r.Method,
		Agent:          agentName,
		ToolName:       toolName,
		ToolParameters: toolParams,
		Message:        prompt,
		Response:       response.Text,
		StartTime:      start,
		Duration:       time.Since(start),
		Status:         "success",
		HTTPCode:       http.StatusOK,
	})

	// Include trace ID in response for client correlation
	w.Header().Set("X-Trace-ID", traceID)
	writeJSON(w, http.StatusOK, response)
}

// recordAudit sends a request to the auditor if configured.
func (g *Gateway) recordAudit(ctx context.Context, req *audit.GatewayRequest) {
	if g.auditor == nil {
		return
	}
	if err := g.auditor.RecordRequest(ctx, req); err != nil {
		slog.Debug("failed to record audit", "error", err)
	}
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
