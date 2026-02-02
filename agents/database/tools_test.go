package main

import "testing"

func TestDiagnosePsqlError(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantSub string // substring expected in the diagnosis (empty = no diagnosis)
	}{
		{
			name:    "database does not exist",
			output:  `FATAL:  database "mydb" does not exist`,
			wantSub: "does not exist on this server",
		},
		{
			name:    "connection refused",
			output:  "could not connect to server: Connection refused",
			wantSub: "Connection refused",
		},
		{
			name:    "unknown host",
			output:  `psql: error: could not translate host name "badhost" to address`,
			wantSub: "hostname in the connection string could not be resolved",
		},
		{
			name:    "password auth failed",
			output:  `FATAL:  password authentication failed for user "postgres"`,
			wantSub: "Authentication failed",
		},
		{
			name:    "no pg_hba.conf entry",
			output:  `FATAL:  no pg_hba.conf entry for host "10.0.0.1"`,
			wantSub: "pg_hba.conf",
		},
		{
			name:    "timeout expired",
			output:  "timeout expired",
			wantSub: "Connection timed out",
		},
		{
			name:    "could not connect",
			output:  "could not connect to server: timed out",
			wantSub: "Connection timed out",
		},
		{
			name:    "role does not exist (caught by does-not-exist case)",
			output:  `FATAL:  role "baduser" does not exist`,
			wantSub: "does not exist on this server",
		},
		{
			name:    "ssl unsupported",
			output:  "SSL connection is unsupported by this server",
			wantSub: "SSL configuration mismatch",
		},
		{
			name:    "ssl required",
			output:  "SSL required by the server",
			wantSub: "SSL configuration mismatch",
		},
		{
			name:    "unknown error returns empty",
			output:  "some completely unrecognized error message",
			wantSub: "",
		},
		{
			name:    "empty output returns empty",
			output:  "",
			wantSub: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnosePsqlError(tt.output)
			if tt.wantSub == "" {
				if got != "" {
					t.Errorf("diagnosePsqlError(%q) = %q, want empty", tt.output, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("diagnosePsqlError(%q) = empty, want substring %q", tt.output, tt.wantSub)
			}
			if !contains(got, tt.wantSub) {
				t.Errorf("diagnosePsqlError(%q) = %q, missing substring %q", tt.output, got, tt.wantSub)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
