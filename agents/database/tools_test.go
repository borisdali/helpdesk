package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// mockRunner implements CommandRunner for testing.
type mockRunner struct {
	output string
	err    error
}

func (m mockRunner) Run(_ context.Context, _ string, _ []string, _ []string) (string, error) {
	return m.output, m.err
}

// withMockRunner temporarily replaces cmdRunner for a test.
func withMockRunner(output string, err error) func() {
	old := cmdRunner
	cmdRunner = mockRunner{output: output, err: err}
	return func() { cmdRunner = old }
}

// mockToolContext implements tool.Context for testing.
type mockToolContext struct {
	context.Context
}

// ReadonlyContext methods
func (mockToolContext) UserContent() *genai.Content         { return nil }
func (mockToolContext) InvocationID() string                { return "test-invocation" }
func (mockToolContext) AgentName() string                   { return "test-agent" }
func (mockToolContext) ReadonlyState() session.ReadonlyState { return nil }
func (mockToolContext) UserID() string                      { return "test-user" }
func (mockToolContext) AppName() string                     { return "test-app" }
func (mockToolContext) SessionID() string                   { return "test-session" }
func (mockToolContext) Branch() string                      { return "" }

// CallbackContext methods
func (mockToolContext) Artifacts() agent.Artifacts { return nil }
func (mockToolContext) State() session.State       { return nil }

// tool.Context methods
func (mockToolContext) FunctionCallID() string                                      { return "test-call-id" }
func (mockToolContext) Actions() *session.EventActions                              { return nil }
func (mockToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) { return nil, nil }
func (mockToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation        { return nil }
func (mockToolContext) RequestConfirmation(string, any) error                       { return nil }

func newTestContext() tool.Context {
	return mockToolContext{context.Background()}
}

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
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("diagnosePsqlError(%q) = %q, missing substring %q", tt.output, got, tt.wantSub)
			}
		})
	}
}

func TestRunPsql_Success(t *testing.T) {
	defer withMockRunner("version | PostgreSQL 16.1\n", nil)()

	ctx := context.Background()
	output, err := runPsql(ctx, "host=localhost", "SELECT version();")
	if err != nil {
		t.Fatalf("runPsql() error = %v, want nil", err)
	}
	if !strings.Contains(output, "PostgreSQL") {
		t.Errorf("runPsql() output = %q, want to contain 'PostgreSQL'", output)
	}
}

func TestRunPsql_Error(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()

	ctx := context.Background()
	_, err := runPsql(ctx, "host=badhost", "SELECT 1;")
	if err == nil {
		t.Fatal("runPsql() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("runPsql() error = %v, want to contain diagnosis", err)
	}
}

func TestRunPsql_EmptyOutput(t *testing.T) {
	defer withMockRunner("", errors.New("exit status 1"))()

	ctx := context.Background()
	_, err := runPsql(ctx, "host=localhost", "SELECT 1;")
	if err == nil {
		t.Fatal("runPsql() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "no output from psql") {
		t.Errorf("runPsql() error = %v, want to contain 'no output from psql'", err)
	}
}

func TestRunPsql_UndiagnosedError(t *testing.T) {
	defer withMockRunner("some weird error", errors.New("exit status 1"))()

	ctx := context.Background()
	_, err := runPsql(ctx, "host=localhost", "SELECT 1;")
	if err == nil {
		t.Fatal("runPsql() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "psql failed") {
		t.Errorf("runPsql() error = %v, want to contain 'psql failed'", err)
	}
}

func TestRunPsql_EmptyConnStr(t *testing.T) {
	defer withMockRunner("version | PostgreSQL 16.1\n", nil)()

	ctx := context.Background()
	output, err := runPsql(ctx, "", "SELECT version();")
	if err != nil {
		t.Fatalf("runPsql() error = %v, want nil", err)
	}
	if output == "" {
		t.Error("runPsql() output is empty")
	}
}

func TestCheckConnectionTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]-----+------------------------------------------
version           | PostgreSQL 16.1 on x86_64-linux-gnu
current_database  | testdb
current_user      | postgres
inet_server_addr  | 127.0.0.1
inet_server_port  | 5432
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := checkConnectionTool(ctx, CheckConnectionArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("checkConnectionTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "Connection successful") {
		t.Errorf("checkConnectionTool() output = %q, want to contain 'Connection successful'", result.Output)
	}
	if !strings.Contains(result.Output, "PostgreSQL 16.1") {
		t.Errorf("checkConnectionTool() output = %q, want to contain 'PostgreSQL 16.1'", result.Output)
	}
}

func TestCheckConnectionTool_Failure(t *testing.T) {
	defer withMockRunner("password authentication failed", errors.New("exit status 1"))()

	ctx := newTestContext()
	_, err := checkConnectionTool(ctx, CheckConnectionArgs{ConnectionString: "host=localhost"})
	if err == nil {
		t.Fatal("checkConnectionTool() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "connection failed") {
		t.Errorf("checkConnectionTool() error = %v, want to contain 'connection failed'", err)
	}
}

func TestGetActiveConnectionsTool_WithConnections(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+----------------------------------------
pid             | 12345
user            | postgres
database        | testdb
client_addr     | 192.168.1.100
state           | active
wait_event_type |
wait_event      |
query_seconds   | 5
query_preview   | SELECT * FROM users WHERE id = 1
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "12345") {
		t.Errorf("getActiveConnectionsTool() output = %q, want to contain pid", result.Output)
	}
}

func TestGetActiveConnectionsTool_NoConnections(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "No active connections found") {
		t.Errorf("getActiveConnectionsTool() output = %q, want 'No active connections found'", result.Output)
	}
}

func TestGetActiveConnectionsTool_EmptyOutput(t *testing.T) {
	defer withMockRunner("   \n  ", nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "No active connections found") {
		t.Errorf("getActiveConnectionsTool() output = %q, want 'No active connections found'", result.Output)
	}
}

func TestGetActiveConnectionsTool_IncludeIdle(t *testing.T) {
	mockOutput := "-[ RECORD 1 ]---+---\npid | 123\nstate | idle\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{
		ConnectionString: "host=localhost",
		IncludeIdle:      true,
	})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if result.Output == "" {
		t.Error("getActiveConnectionsTool() output is empty")
	}
}

func TestGetLockInfoTool_WithLocks(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]--+----------------------------------------
blocked_pid    | 12345
blocked_user   | app_user
blocking_pid   | 12346
blocking_user  | admin
blocked_query  | UPDATE users SET name = 'foo'
blocking_query | ALTER TABLE users ADD COLUMN bar TEXT
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getLockInfoTool(ctx, GetLockInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getLockInfoTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "blocked_pid") {
		t.Errorf("getLockInfoTool() output = %q, want lock info", result.Output)
	}
}

func TestGetLockInfoTool_NoLocks(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := getLockInfoTool(ctx, GetLockInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getLockInfoTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "No blocking locks found") {
		t.Errorf("getLockInfoTool() output = %q, want 'No blocking locks found'", result.Output)
	}
}

func TestGetLockInfoTool_EmptyOutput(t *testing.T) {
	defer withMockRunner("", nil)()

	ctx := newTestContext()
	result, err := getLockInfoTool(ctx, GetLockInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getLockInfoTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "No blocking locks found") {
		t.Errorf("getLockInfoTool() output = %q, want 'No blocking locks found'", result.Output)
	}
}

func TestGetTableStatsTool_WithTableName(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]----+----------------------
schemaname       | public
table_name       | users
total_size       | 1024 kB
live_rows        | 1000
dead_rows        | 50
dead_ratio       | 4.76
last_vacuum      | 2024-01-15 10:30:00
last_autovacuum  | 2024-01-15 12:00:00
last_analyze     | 2024-01-15 10:30:00
last_autoanalyze | 2024-01-15 12:00:00
seq_scan         | 100
idx_scan         | 5000
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getTableStatsTool(ctx, GetTableStatsArgs{
		ConnectionString: "host=localhost",
		TableName:        "users",
	})
	if err != nil {
		t.Fatalf("getTableStatsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "users") {
		t.Errorf("getTableStatsTool() output = %q, want to contain 'users'", result.Output)
	}
	if !strings.Contains(result.Output, "dead_rows") {
		t.Errorf("getTableStatsTool() output = %q, want to contain 'dead_rows'", result.Output)
	}
}

func TestGetTableStatsTool_CustomSchema(t *testing.T) {
	mockOutput := "-[ RECORD 1 ]---+---\ntable_name | my_table\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getTableStatsTool(ctx, GetTableStatsArgs{
		ConnectionString: "host=localhost",
		SchemaName:       "custom_schema",
	})
	if err != nil {
		t.Fatalf("getTableStatsTool() error = %v, want nil", err)
	}
	if result.Output == "" {
		t.Error("getTableStatsTool() output is empty")
	}
}

func TestGetTableStatsTool_DefaultSchema(t *testing.T) {
	mockOutput := "-[ RECORD 1 ]---+---\ntable_name | public_table\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getTableStatsTool(ctx, GetTableStatsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getTableStatsTool() error = %v, want nil", err)
	}
	if result.Output == "" {
		t.Error("getTableStatsTool() output is empty")
	}
}

func TestGetDatabaseInfoTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]----+---------
database         | testdb
size             | 50 MB
owner            | postgres
encoding         | UTF8
collation        | en_US.UTF-8
connection_limit | -1
in_recovery      | No
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getDatabaseInfoTool(ctx, GetDatabaseInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getDatabaseInfoTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "testdb") {
		t.Errorf("getDatabaseInfoTool() output = %q, want to contain 'testdb'", result.Output)
	}
}

func TestGetConnectionStatsTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]--------+--------
database             | testdb
total_connections    | 10
active               | 2
idle                 | 7
idle_in_transaction  | 1
waiting_on_lock      | 0
max_connections      | 100
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getConnectionStatsTool(ctx, GetConnectionStatsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getConnectionStatsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "total_connections") {
		t.Errorf("getConnectionStatsTool() output = %q, want connection stats", result.Output)
	}
}

func TestGetDatabaseStatsTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+---------
database        | testdb
connections     | 5
commits         | 10000
rollbacks       | 10
blocks_read     | 5000
cache_hits      | 95000
cache_hit_ratio | 95.00
rows_returned   | 50000
rows_fetched    | 40000
rows_inserted   | 1000
rows_updated    | 500
rows_deleted    | 100
conflicts       | 0
deadlocks       | 0
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getDatabaseStatsTool(ctx, GetDatabaseStatsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getDatabaseStatsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "cache_hit_ratio") {
		t.Errorf("getDatabaseStatsTool() output = %q, want database stats", result.Output)
	}
}

func TestGetConfigParameterTool_SpecificParameter(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]+-------------------------------
name       | max_connections
setting    | 100
unit       |
short_desc | Sets the maximum number of concurrent connections.
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getConfigParameterTool(ctx, GetConfigParameterArgs{
		ConnectionString: "host=localhost",
		ParameterName:    "max_connections",
	})
	if err != nil {
		t.Fatalf("getConfigParameterTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "max_connections") {
		t.Errorf("getConfigParameterTool() output = %q, want 'max_connections'", result.Output)
	}
}

func TestGetConfigParameterTool_DefaultParameters(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]+---
name       | max_connections
setting    | 100
-[ RECORD 2 ]+---
name       | shared_buffers
setting    | 128MB
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getConfigParameterTool(ctx, GetConfigParameterArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getConfigParameterTool() error = %v, want nil", err)
	}
	if result.Output == "" {
		t.Error("getConfigParameterTool() output is empty")
	}
}

func TestGetReplicationStatusTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+--------
role            | Primary
is_in_recovery  | f

-[ RECORD 1 ]---+---------------
client_addr     | 192.168.1.101
user            | replicator
application_name| replica1
state           | streaming
sync_state      | async
write_lag_bytes | 0
flush_lag_bytes | 0
replay_lag_bytes| 1024
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getReplicationStatusTool(ctx, GetReplicationStatusArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getReplicationStatusTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "Primary") {
		t.Errorf("getReplicationStatusTool() output = %q, want replication status", result.Output)
	}
}

// Test error handling for all tools
func TestToolsErrorHandling(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()
	ctx := newTestContext()

	tests := []struct {
		name string
		fn   func() error
	}{
		{"getDatabaseInfoTool", func() error { _, err := getDatabaseInfoTool(ctx, GetDatabaseInfoArgs{}); return err }},
		{"getActiveConnectionsTool", func() error { _, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{}); return err }},
		{"getConnectionStatsTool", func() error { _, err := getConnectionStatsTool(ctx, GetConnectionStatsArgs{}); return err }},
		{"getDatabaseStatsTool", func() error { _, err := getDatabaseStatsTool(ctx, GetDatabaseStatsArgs{}); return err }},
		{"getConfigParameterTool", func() error { _, err := getConfigParameterTool(ctx, GetConfigParameterArgs{}); return err }},
		{"getReplicationStatusTool", func() error { _, err := getReplicationStatusTool(ctx, GetReplicationStatusArgs{}); return err }},
		{"getLockInfoTool", func() error { _, err := getLockInfoTool(ctx, GetLockInfoArgs{}); return err }},
		{"getTableStatsTool", func() error { _, err := getTableStatsTool(ctx, GetTableStatsArgs{}); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Errorf("%s() error = nil, want error", tt.name)
			}
		})
	}
}
