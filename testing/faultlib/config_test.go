package faultlib

import "testing"

func TestHostPortOnly(t *testing.T) {
	cases := []struct {
		name    string
		connStr string
		want    string
	}{
		{
			name:    "typical libpq connection string",
			connStr: "host=host.docker.internal port=15433 dbname=testdb user=postgres password=testpass",
			want:    "host.docker.internal:15433",
		},
		{
			name:    "host and port only, no other fields",
			connStr: "host=localhost port=5432",
			want:    "localhost:5432",
		},
		{
			name:    "fields in reverse order",
			connStr: "dbname=testdb port=5433 user=postgres host=127.0.0.1",
			want:    "127.0.0.1:5433",
		},
		{
			name:    "missing port returns empty",
			connStr: "host=localhost dbname=testdb",
			want:    "",
		},
		{
			name:    "missing host returns empty",
			connStr: "port=5432 dbname=testdb",
			want:    "",
		},
		{
			name:    "empty connection string returns empty",
			connStr: "",
			want:    "",
		},
		{
			name:    "malformed garbage returns empty",
			connStr: "not a connection string at all",
			want:    "",
		},
		{
			name:    "extra whitespace between fields",
			connStr: "  host=host.docker.internal   port=15433  ",
			want:    "host.docker.internal:15433",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostPortOnly(tc.connStr); got != tc.want {
				t.Errorf("hostPortOnly(%q) = %q, want %q", tc.connStr, got, tc.want)
			}
		})
	}
}

func TestResolvePrompt_ReplicaHostPortSubstitution(t *testing.T) {
	cfg := &HarnessConfig{
		ConnStr:        "host=primary port=5432 dbname=testdb",
		ReplicaConnStr: "host=host.docker.internal port=15433 dbname=testdb user=postgres password=testpass",
	}
	got := ResolvePrompt("target: {{replica_host_port}}", cfg)
	want := "target: host.docker.internal:15433"
	if got != want {
		t.Errorf("ResolvePrompt() = %q, want %q", got, want)
	}
}

func TestResolvePrompt_ReplicaHostPort_NoReplicaConfigured(t *testing.T) {
	cfg := &HarnessConfig{ConnStr: "host=primary port=5432 dbname=testdb"}
	got := ResolvePrompt("target: {{replica_host_port}}", cfg)
	want := "target: "
	if got != want {
		t.Errorf("ResolvePrompt() = %q, want %q (empty substitution when no replica configured)", got, want)
	}
}
