// Command faulttest injects database and Kubernetes failure modes, sends
// diagnostic prompts to helpdesk agents, and evaluates their responses.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"helpdesk/testing/testutil"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "list":
		cmdList(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "inject":
		cmdInject(os.Args[2:])
	case "teardown":
		cmdTeardown(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: faulttest <command> [options]

Commands:
  list       List all failure modes in the catalog
  run        Inject failures, run agent, evaluate, teardown
  inject     Inject a specific failure (interactive mode)
  teardown   Tear down a specific failure (interactive mode)
`)
}

// defaultTestingDir returns the testing/ directory relative to the binary or cwd.
func defaultTestingDir() string {
	// Try relative to cwd.
	if _, err := os.Stat("testing/catalog/failures.yaml"); err == nil {
		return "testing"
	}
	// Try parent.
	if _, err := os.Stat("../catalog/failures.yaml"); err == nil {
		return ".."
	}
	return "testing"
}

func loadConfig(fs *flag.FlagSet, args []string) *HarnessConfig {
	cfg := &HarnessConfig{}

	fs.StringVar(&cfg.TestingDir, "testing-dir", defaultTestingDir(), "Path to the testing/ directory")
	fs.StringVar(&cfg.ConnStr, "conn", "", "PostgreSQL connection string")
	fs.StringVar(&cfg.ReplicaConnStr, "replica-conn", "", "Replica PostgreSQL connection string")
	fs.StringVar(&cfg.DBAgentURL, "db-agent", "", "Database agent A2A URL")
	fs.StringVar(&cfg.K8sAgentURL, "k8s-agent", "", "Kubernetes agent A2A URL")
	fs.StringVar(&cfg.OrchestratorURL, "orchestrator", "", "Orchestrator agent A2A URL")
	fs.StringVar(&cfg.KubeContext, "context", "", "Kubernetes context")

	var categories, ids string
	fs.StringVar(&categories, "categories", "", "Comma-separated categories to test (database,kubernetes,compound)")
	fs.StringVar(&ids, "ids", "", "Comma-separated failure IDs to test")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if categories != "" {
		cfg.Categories = strings.Split(categories, ",")
	}
	if ids != "" {
		cfg.FailureIDs = strings.Split(ids, ",")
	}

	cfg.CatalogPath = filepath.Join(cfg.TestingDir, "catalog", "failures.yaml")
	testutil.DockerComposeDir = filepath.Join(cfg.TestingDir, "docker")

	return cfg
}

// ── list ─────────────────────────────────────────────────────────────────

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	catalog, err := LoadCatalog(cfg.CatalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	failures := FilterFailures(catalog, cfg.Categories, cfg.FailureIDs)

	fmt.Printf("%-30s %-12s %-10s %s\n", "ID", "CATEGORY", "SEVERITY", "NAME")
	fmt.Println(strings.Repeat("-", 80))
	for _, f := range failures {
		fmt.Printf("%-30s %-12s %-10s %s\n", f.ID, f.Category, f.Severity, f.Name)
	}
	fmt.Printf("\nTotal: %d failure modes\n", len(failures))
}

// ── run ──────────────────────────────────────────────────────────────────

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg := loadConfig(fs, args)

	catalog, err := LoadCatalog(cfg.CatalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	failures := FilterFailures(catalog, cfg.Categories, cfg.FailureIDs)
	if len(failures) == 0 {
		fmt.Fprintln(os.Stderr, "No failures match the specified filters.")
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)
	runner := NewRunner(cfg)

	runID := uuid.New().String()[:8]
	var results []EvalResult

	for _, f := range failures {
		fmt.Printf("\n--- Testing: %s (%s) ---\n", f.Name, f.ID)

		// Save original conn string for config-override failures.
		origConn := cfg.ConnStr

		// 1. Inject.
		if err := injector.Inject(ctx, f); err != nil {
			slog.Error("injection failed", "id", f.ID, "err", err)
			results = append(results, EvalResult{
				FailureID:   f.ID,
				FailureName: f.Name,
				Category:    f.Category,
				Error:       fmt.Sprintf("injection failed: %v", err),
			})
			cfg.ConnStr = origConn
			continue
		}

		// 2. Run agent.
		resp := runner.Run(ctx, f)

		// 3. Evaluate.
		var evalResult EvalResult
		if resp.Error != nil {
			evalResult = EvalResult{
				FailureID:   f.ID,
				FailureName: f.Name,
				Category:    f.Category,
				Error:       resp.Error.Error(),
				Duration:    resp.Duration.String(),
			}
		} else {
			evalResult = Evaluate(f, resp.Text)
			evalResult.ResponseText = resp.Text
			evalResult.Duration = resp.Duration.String()
		}
		results = append(results, evalResult)

		// 4. Teardown.
		cfg.ConnStr = origConn
		if err := injector.Teardown(ctx, f); err != nil {
			slog.Error("teardown failed", "id", f.ID, "err", err)
		}

		status := "PASS"
		if !evalResult.Passed {
			status = "FAIL"
		}
		fmt.Printf("Result: [%s] score=%d%%\n", status, int(evalResult.Score*100))
	}

	report := BuildReport(runID, results)
	report.PrintSummary()

	reportFile := fmt.Sprintf("faulttest-%s.json", runID)
	if err := report.WriteJSON(reportFile); err != nil {
		slog.Error("failed to write report", "err", err)
	} else {
		fmt.Printf("Report written to %s\n", reportFile)
	}
}

// ── inject ───────────────────────────────────────────────────────────────

func cmdInject(args []string) {
	fs := flag.NewFlagSet("inject", flag.ExitOnError)
	var failureID string
	fs.StringVar(&failureID, "id", "", "Failure ID to inject")
	cfg := loadConfig(fs, args)

	if failureID == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	catalog, err := LoadCatalog(cfg.CatalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	f := findFailure(catalog, failureID)
	if f == nil {
		fmt.Fprintf(os.Stderr, "Error: failure %q not found\n", failureID)
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)

	if err := injector.Inject(ctx, *f); err != nil {
		fmt.Fprintf(os.Stderr, "Injection failed: %v\n", err)
		os.Exit(1)
	}

	prompt := ResolvePrompt(f.Prompt, cfg)
	fmt.Printf("Failure injected: %s\n\n", f.Name)
	fmt.Printf("Suggested prompt for the agent:\n%s\n", prompt)
	fmt.Printf("\nTo tear down: faulttest teardown --id %s [same flags]\n", f.ID)
}

// ── teardown ─────────────────────────────────────────────────────────────

func cmdTeardown(args []string) {
	fs := flag.NewFlagSet("teardown", flag.ExitOnError)
	var failureID string
	fs.StringVar(&failureID, "id", "", "Failure ID to tear down")
	cfg := loadConfig(fs, args)

	if failureID == "" {
		fmt.Fprintln(os.Stderr, "Error: --id is required")
		os.Exit(1)
	}

	catalog, err := LoadCatalog(cfg.CatalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	f := findFailure(catalog, failureID)
	if f == nil {
		fmt.Fprintf(os.Stderr, "Error: failure %q not found\n", failureID)
		os.Exit(1)
	}

	ctx := context.Background()
	injector := NewInjector(cfg)

	if err := injector.Teardown(ctx, *f); err != nil {
		fmt.Fprintf(os.Stderr, "Teardown failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Failure torn down: %s\n", f.Name)
}

func findFailure(catalog *Catalog, id string) *Failure {
	for i := range catalog.Failures {
		if catalog.Failures[i].ID == id {
			return &catalog.Failures[i]
		}
	}
	return nil
}
