package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeInfraConfig writes a minimal infrastructure.json to a temp dir and
// returns the file path.
func writeInfraConfig(t *testing.T, content any) string {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "infrastructure.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestCheckTargetSafety_Disabled(t *testing.T) {
	// Empty infraConfigPath → always passes, no file read.
	if err := checkTargetSafety("", "host=prod port=5432"); err != nil {
		t.Errorf("expected nil when infraConfigPath is empty, got %v", err)
	}
}

func TestCheckTargetSafety_TestTag(t *testing.T) {
	cfg := map[string]any{
		"db_servers": map[string]any{
			"test-db": map[string]any{
				"connection_string": "host=test-host port=5432 dbname=testdb user=postgres",
				"tags":              []string{"test", "development"},
			},
		},
	}
	path := writeInfraConfig(t, cfg)

	err := checkTargetSafety(path, "host=test-host port=5432 dbname=testdb")
	if err != nil {
		t.Errorf("expected nil for server tagged 'test', got %v", err)
	}
}

func TestCheckTargetSafety_ChaosTag(t *testing.T) {
	cfg := map[string]any{
		"db_servers": map[string]any{
			"chaos-db": map[string]any{
				"connection_string": "host=chaos-host port=5432 dbname=db user=postgres",
				"tags":              []string{"chaos"},
			},
		},
	}
	path := writeInfraConfig(t, cfg)

	err := checkTargetSafety(path, "host=chaos-host port=5432")
	if err != nil {
		t.Errorf("expected nil for server tagged 'chaos', got %v", err)
	}
}

func TestCheckTargetSafety_NoTag_Denied(t *testing.T) {
	cfg := map[string]any{
		"db_servers": map[string]any{
			"prod-db": map[string]any{
				"connection_string": "host=prod-host port=5432 dbname=proddb user=postgres",
				"tags":              []string{"production"},
			},
		},
	}
	path := writeInfraConfig(t, cfg)

	err := checkTargetSafety(path, "host=prod-host port=5432")
	if err == nil {
		t.Error("expected error for server without test/chaos tag, got nil")
	}
}

func TestCheckTargetSafety_HostNotInConfig_Denied(t *testing.T) {
	cfg := map[string]any{
		"db_servers": map[string]any{
			"other-db": map[string]any{
				"connection_string": "host=other-host port=5432 dbname=db user=postgres",
				"tags":              []string{"test"},
			},
		},
	}
	path := writeInfraConfig(t, cfg)

	err := checkTargetSafety(path, "host=unknown-host port=5432")
	if err == nil {
		t.Error("expected error for host not in infra config, got nil")
	}
}

func TestCheckTargetSafety_MissingFile(t *testing.T) {
	err := checkTargetSafety("/nonexistent/infra.json", "host=test port=5432")
	if err == nil {
		t.Error("expected error for missing infra config file, got nil")
	}
}

func TestConnStrHost_DSN(t *testing.T) {
	tests := []struct {
		connStr string
		want    string
	}{
		{"host=myhost port=5432 dbname=prod user=postgres", "myhost"},
		{"host=192.168.1.1 port=5432", "192.168.1.1"},
		{"dbname=prod user=postgres", ""},  // no host field
		{"", ""},
	}

	for _, tt := range tests {
		got := connStrHost(tt.connStr)
		if got != tt.want {
			t.Errorf("connStrHost(%q) = %q, want %q", tt.connStr, got, tt.want)
		}
	}
}

func TestConnStrHost_URL(t *testing.T) {
	tests := []struct {
		connStr string
		want    string
	}{
		{"postgres://myhost:5432/prod", "myhost"},
		{"postgresql://user:pass@db.example.com:5432/mydb", "db.example.com"},
		{"postgres://localhost/testdb", "localhost"},
	}

	for _, tt := range tests {
		got := connStrHost(tt.connStr)
		if got != tt.want {
			t.Errorf("connStrHost(%q) = %q, want %q", tt.connStr, got, tt.want)
		}
	}
}

func TestCheckTargetSafety_URLConnStr(t *testing.T) {
	cfg := map[string]any{
		"db_servers": map[string]any{
			"url-db": map[string]any{
				"connection_string": "host=url-host port=5432 dbname=db user=postgres",
				"tags":              []string{"test"},
			},
		},
	}
	path := writeInfraConfig(t, cfg)

	// Target provided as URL format; infra stores it as DSN — host should match.
	err := checkTargetSafety(path, "postgres://url-host:5432/db")
	if err != nil {
		t.Errorf("expected nil when host matches via URL connstr, got %v", err)
	}
}
