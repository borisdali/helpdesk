// Package main implements the REST gateway — a thin HTTP layer that translates
// REST requests into A2A JSON-RPC calls to the helpdesk sub-agents.
// The gateway itself uses an LLM only for the fleet job planner (POST /api/v1/fleet/plan);
// all other AI reasoning is delegated to the sub-agents.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/a2a"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/authz"
	"helpdesk/internal/buildinfo"
	"helpdesk/internal/discovery"
	"helpdesk/internal/identity"
	"helpdesk/internal/infra"
	"helpdesk/internal/logging"
	"helpdesk/internal/toolregistry"
)

func main() {
	logging.InitLogging(os.Args[1:])
	slog.Info("helpdesk gateway", "version", buildinfo.Version)

	listenAddr := os.Getenv("HELPDESK_GATEWAY_ADDR")
	if listenAddr == "" {
		listenAddr = "localhost:8080"
	}

	agentURLs := os.Getenv("HELPDESK_AGENT_URLS")
	if agentURLs == "" {
		slog.Error("HELPDESK_AGENT_URLS is required (comma-separated agent base URLs)")
		os.Exit(1)
	}

	urls := strings.Split(agentURLs, ",")
	for i := range urls {
		urls[i] = strings.TrimSpace(urls[i])
	}

	discoveryTimeout := 60 * time.Second
	if v := os.Getenv("HELPDESK_DISCOVERY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			discoveryTimeout = d
		} else {
			slog.Warn("invalid HELPDESK_DISCOVERY_TIMEOUT, using default", "value", v, "default", discoveryTimeout)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	registry, err := discovery.DiscoverWithRetry(ctx, urls, 3*time.Second)
	if err != nil {
		slog.Error("failed to discover agents", "err", err)
		os.Exit(1)
	}

	gw := NewGateway(registry)

	// Build tool registry from discovered agent cards and schemas.
	agentCards := make(map[string]*a2a.AgentCard, len(registry))
	agentSchemas := make(map[string]map[string]map[string]any, len(registry))
	for name, agent := range registry {
		agentCards[name] = agent.Card
		if agent.Schemas != nil {
			agentSchemas[name] = agent.Schemas
		}
	}
	toolReg := toolregistry.Build(agentCards, agentSchemas, audit.ToolClassification)
	gw.SetToolRegistry(toolReg)
	slog.Info("tool registry built", "tools", len(toolReg.List()))

	// Initialize identity provider.
	idProvider, err := identity.NewFromEnv()
	if err != nil {
		slog.Error("failed to initialize identity provider", "err", err)
		os.Exit(1)
	}
	gw.SetIdentityProvider(idProvider)
	gw.SetUsersFile(os.Getenv("HELPDESK_USERS_FILE"))
	gw.SetOperatingMode(os.Getenv("HELPDESK_OPERATING_MODE"))
	slog.Info("identity provider initialized", "mode", os.Getenv("HELPDESK_IDENTITY_PROVIDER"))

	// Build central authorizer. Enforcement is active when a real identity
	// provider is configured (anything other than "none" or empty).
	idMode := os.Getenv("HELPDESK_IDENTITY_PROVIDER")
	enforcing := idMode != "" && idMode != "none"
	authzr := authz.NewAuthorizer(authz.DefaultGatewayPermissions, enforcing)
	slog.Info("authorization configured", "enforcing", enforcing)

	// Initialize audit store if enabled.
	auditURL := os.Getenv("HELPDESK_AUDIT_URL")
	auditAPIKey := os.Getenv("HELPDESK_AUDIT_API_KEY")
	auditEnabled := os.Getenv("HELPDESK_AUDIT_ENABLED") == "true" || os.Getenv("HELPDESK_AUDIT_ENABLED") == "1"

	// Always set audit URL for governance queries (even if audit logging is disabled)
	if auditURL != "" {
		gw.SetAuditURL(auditURL)
		slog.Info("governance queries enabled", "url", auditURL)
	}
	if auditAPIKey != "" {
		gw.SetAuditAPIKey(auditAPIKey)
	}
	if agentAPIKey := os.Getenv("HELPDESK_AGENT_API_KEY"); agentAPIKey != "" {
		gw.SetAgentAPIKey(agentAPIKey)
		slog.Info("agent inbound auth configured (HELPDESK_AGENT_API_KEY set)")
	} else {
		slog.Warn("HELPDESK_AGENT_API_KEY not set — gateway will not authenticate to agent /tool/{name} endpoints")
	}

	if auditEnabled {
		var auditor audit.Auditor
		if auditURL != "" {
			// Use central audit service (preferred)
			remoteStore := audit.NewRemoteStore(auditURL)
			if auditAPIKey != "" {
				remoteStore = remoteStore.WithAPIKey(auditAPIKey)
			}
			auditor = remoteStore
			slog.Info("audit logging enabled (remote)", "url", auditURL)
		} else {
			// Fall back to local store with socket (legacy mode)
			auditDir := os.Getenv("HELPDESK_AUDIT_DIR")
			if auditDir == "" {
				auditDir = "."
			}
			auditCfg := audit.StoreConfig{
				DBPath:     filepath.Join(auditDir, "audit.db"),
				SocketPath: filepath.Join(auditDir, "audit.sock"),
			}
			localStore, err := audit.NewStore(auditCfg)
			if err != nil {
				slog.Error("failed to initialize audit store", "err", err)
				os.Exit(1)
			}
			slog.Info("audit logging enabled (local)", "db", auditCfg.DBPath, "socket", auditCfg.SocketPath)
			auditor = localStore

			// Start an embedded governance server so that /api/v1/governance/*
			// proxy endpoints work in local mode, identical to remote auditd mode.
			govURL, err := startLocalGovernanceServer(localStore)
			if err != nil {
				slog.Warn("failed to start local governance server; governance queries unavailable", "err", err)
			} else {
				gw.SetAuditURL(govURL)
				slog.Info("local governance server started", "url", govURL)
			}
		}
		defer auditor.Close()

		gw.SetAuditor(audit.NewGatewayAuditor(auditor))
	}

	// Load infrastructure config if available.
	if infraPath := os.Getenv("HELPDESK_INFRA_CONFIG"); infraPath != "" {
		infraConfig, err := infra.Load(infraPath)
		if err != nil {
			slog.Warn("failed to load infrastructure config", "path", infraPath, "err", err)
		} else {
			gw.SetInfraConfig(infraConfig)
			slog.Info("infrastructure config loaded",
				"path", infraPath,
				"db_servers", len(infraConfig.DBServers),
				"k8s_clusters", len(infraConfig.K8sClusters),
				"vms", len(infraConfig.VMs))
		}
	} else {
		slog.Warn("HELPDESK_INFRA_CONFIG not set — fleet planner (POST /api/v1/fleet/plan) will return 503")
	}

	// Initialize fleet planner LLM (vendor-agnostic via agentutil).
	plannerAPIKey := os.Getenv("HELPDESK_API_KEY")
	if plannerAPIKey == "" {
		plannerAPIKey = os.Getenv("ANTHROPIC_API_KEY") // convenience fallback
	}
	plannerCfg := agentutil.Config{
		ModelVendor: os.Getenv("HELPDESK_MODEL_VENDOR"),
		ModelName:   os.Getenv("HELPDESK_MODEL_NAME"),
		APIKey:      plannerAPIKey,
	}
	if plannerCfg.ModelName == "" {
		plannerCfg.ModelName = "claude-haiku-4-5-20251001"
	}
	if plannerCfg.ModelVendor == "" || plannerCfg.APIKey == "" {
		slog.Warn("fleet planner LLM not configured (HELPDESK_MODEL_VENDOR or HELPDESK_API_KEY not set) — POST /api/v1/fleet/plan will return 503")
	} else {
		completer, err := agentutil.NewTextCompleter(context.Background(), plannerCfg)
		if err != nil {
			slog.Warn("fleet planner LLM initialization failed — POST /api/v1/fleet/plan will return 503", "err", err)
		} else {
			gw.SetPlannerLLM(completer)
			slog.Info("fleet planner LLM configured", "vendor", plannerCfg.ModelVendor, "model", plannerCfg.ModelName)
		}
	}

	gw.SetAuthorizer(authzr)

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	slog.Info("starting REST gateway", "addr", listenAddr, "agents", len(registry))
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		slog.Error("gateway stopped", "err", err)
		os.Exit(1)
	}
}
