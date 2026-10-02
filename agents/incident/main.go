// Package main implements the incident diagnostic bundle agent.
// It collects fresh diagnostic data from multiple infrastructure layers
// (database, Kubernetes, OS, storage) and packages them into a .tar.gz
// bundle for vendor support.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/google/uuid"
	"google.golang.org/adk/agent/llmagent"

	"helpdesk/agentutil"
	agentserve "helpdesk/agentutil/serve"
	"helpdesk/internal/audit"
	"helpdesk/prompts"
)

func main() {
	cfg := agentutil.MustLoadConfig("localhost:1104")
	ctx := context.Background()

	// Enforce governance compliance in fix mode before any other initialization.
	agentutil.EnforceFixMode(ctx, agentutil.CheckFixModeViolations(cfg), "incident_agent", cfg.AuditURL)

	auditStore, err := agentserve.InitAuditStore(cfg)
	if err != nil {
		slog.Error("failed to initialize audit store", "err", err)
		os.Exit(1)
	}
	if auditStore != nil {
		defer func() { _ = auditStore.Close() }()
	}
	currentTraceStore = &audit.CurrentTraceStore{}
	traceStore := currentTraceStore
	if auditStore != nil {
		sessionID := "incident_" + uuid.New().String()[:8]
		toolAuditor = audit.NewToolAuditorWithTraceStore(auditStore, "incident_agent", sessionID, currentTraceStore)
		slog.Info("tool auditing enabled", "session_id", sessionID)
	}

	// Initialize policy engine if configured — was entirely absent until
	// 2026-09-30, when this whole block didn't exist and the line below read
	// a hardcoded "policy": false literal instead of cfg.PolicyEnabled.
	// Mirrors agents/database and agents/sysadmin's own policy wiring.
	policyEngine, err := agentutil.InitPolicyEngine(cfg)
	if err != nil {
		slog.Error("failed to initialize policy engine", "err", err)
		os.Exit(1)
	}

	approvalClient := agentserve.InitApprovalClient(cfg)

	policyEnforcer = agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{
		Engine:                     policyEngine,
		PolicyCheckURL:             cfg.PolicyCheckURL,
		PolicyCheckAPIKey:          cfg.AuditAPIKey,
		TraceStore:                 traceStore,
		ApprovalClient:             approvalClient,
		ApprovalTimeout:            cfg.ApprovalTimeout,
		AgentName:                  "incident_agent",
		ToolAuditor:                toolAuditor,
		RequirePurposeForSensitive: os.Getenv("HELPDESK_REQUIRE_PURPOSE_FOR_SENSITIVE") == "true",
	})

	slog.Info("governance", "audit", cfg.AuditEnabled, "policy", cfg.PolicyEnabled)

	llmModel, err := agentutil.NewLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to create LLM model", "err", err)
		os.Exit(1)
	}

	tools, err := createTools()
	if err != nil {
		slog.Error("failed to create tools", "err", err)
		os.Exit(1)
	}

	incidentAgent, err := llmagent.New(llmagent.Config{
		Name:        "incident_agent",
		Description: "Incident diagnostic bundle agent that collects data from database, Kubernetes, OS, and storage layers and packages it into a tarball for vendor support.",
		Instruction: prompts.Incident,
		Model:       llmModel,
		Tools:       tools,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			agentutil.NewReasoningCallback(toolAuditor),
		},
	})
	if err != nil {
		slog.Error("failed to create incident agent", "err", err)
		os.Exit(1)
	}

	cardOpts := agentutil.CardOptions{
		Version:  "1.0.0",
		Provider: &a2a.AgentProvider{Org: "Helpdesk"},
		SkillTags: map[string][]string{
			"incident_agent":                        {"incident", "diagnostics", "bundle"},
			"incident_agent-create_incident_bundle": {"incident", "bundle", "diagnostics", "tarball"},
			"incident_agent-list_incidents":         {"incident", "listing", "history"},
		},
		SkillExamples: map[string][]string{
			"incident_agent-create_incident_bundle": {
				"Create a diagnostic bundle for the production database",
				"Collect incident data for the database running on Kubernetes",
			},
			"incident_agent-list_incidents": {"Show me all previous incident bundles"},
		},
	}

	if err := agentserve.ServeWithTracingAndDirectTools(ctx, incidentAgent, cfg, traceStore, auditStore, NewIncidentDirectRegistry(), cardOpts); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
