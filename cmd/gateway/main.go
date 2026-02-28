// Package main implements the REST gateway — a thin HTTP layer that translates
// REST requests into A2A JSON-RPC calls to the helpdesk sub-agents.
// No LLM is needed in the gateway itself; sub-agents handle AI reasoning.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"helpdesk/internal/audit"
	"helpdesk/internal/discovery"
	"helpdesk/internal/infra"
	"helpdesk/internal/logging"
)

func main() {
	logging.InitLogging(os.Args[1:])

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

	registry, err := discovery.Discover(urls)
	if err != nil {
		slog.Error("failed to discover agents", "err", err)
		os.Exit(1)
	}

	gw := NewGateway(registry)

	// Initialize audit store if enabled.
	auditURL := os.Getenv("HELPDESK_AUDIT_URL")
	auditEnabled := os.Getenv("HELPDESK_AUDIT_ENABLED") == "true" || os.Getenv("HELPDESK_AUDIT_ENABLED") == "1"

	// Always set audit URL for governance queries (even if audit logging is disabled)
	if auditURL != "" {
		gw.SetAuditURL(auditURL)
		slog.Info("governance queries enabled", "url", auditURL)
	}

	if auditEnabled {
		var auditor audit.Auditor
		if auditURL != "" {
			// Use central audit service (preferred)
			auditor = audit.NewRemoteStore(auditURL)
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
	}

	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)

	slog.Info("starting REST gateway", "addr", listenAddr, "agents", len(registry))
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		slog.Error("gateway stopped", "err", err)
		os.Exit(1)
	}
}
