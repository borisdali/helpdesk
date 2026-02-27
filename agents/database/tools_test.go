package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"helpdesk/agentutil"
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

func TestParseRowsAffected(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{name: "DELETE", output: "DELETE 150\n", want: 150},
		{name: "UPDATE", output: "UPDATE 42\n", want: 42},
		{name: "INSERT", output: "INSERT 0 5\n", want: 5},
		{name: "INSERT single row", output: "INSERT 0 1\n", want: 1},
		{name: "DELETE embedded in expanded output", output: "-[ RECORD 1 ]---\ncol | val\nDELETE 7\n", want: 7},
		{name: "zero rows deleted", output: "DELETE 0\n", want: 0},
		{name: "SELECT returns nothing", output: "-[ RECORD 1 ]---\ncol | val\n", want: 0},
		{name: "empty output", output: "", want: 0},
		{name: "unrelated output", output: "ERROR: relation does not exist\n", want: 0},
		{name: "verb prefix but no number", output: "DELETE \n", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRowsAffected(tt.output)
			if got != tt.want {
				t.Errorf("parseRowsAffected(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
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
	result, err := checkConnectionTool(ctx, CheckConnectionArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("checkConnectionTool() unexpected Go error: %v", err)
	}
	// Errors are now returned in the output, not as Go errors
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("checkConnectionTool() output = %q, want to contain 'ERROR'", result.Output)
	}
	if !strings.Contains(result.Output, "check_connection") {
		t.Errorf("checkConnectionTool() output = %q, want to contain 'check_connection'", result.Output)
	}
}

func TestGetServerInfoTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]------+------------------------------
version            | PostgreSQL 16.1
server_started     | 2024-01-15 08:30:00+00
uptime             | 3 days 14:25:33
data_directory     | /var/lib/postgresql/16/main
config_file        | /etc/postgresql/16/main/postgresql.conf
current_db_size    | 250 MB
role               | primary
total_connections  | 15
active_connections | 3
max_connections    | 100
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getServerInfoTool(ctx, GetServerInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getServerInfoTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "uptime") {
		t.Errorf("getServerInfoTool() output = %q, want to contain 'uptime'", result.Output)
	}
	if !strings.Contains(result.Output, "primary") {
		t.Errorf("getServerInfoTool() output = %q, want to contain 'primary'", result.Output)
	}
}

func TestGetServerInfoTool_Failure(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()

	ctx := newTestContext()
	result, err := getServerInfoTool(ctx, GetServerInfoArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getServerInfoTool() unexpected Go error: %v", err)
	}
	// Errors are now returned in the output, not as Go errors
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("getServerInfoTool() output = %q, want to contain 'ERROR'", result.Output)
	}
	if !strings.Contains(result.Output, "get_server_info") {
		t.Errorf("getServerInfoTool() output = %q, want to contain 'get_server_info'", result.Output)
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

func TestGetActiveConnectionsTool_IdleIncludedByDefault(t *testing.T) {
	// Idle sessions (psql at prompt) must appear in the default output.
	mockOutput := "-[ RECORD 1 ]---+---\npid | 123\nstate | idle\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{ConnectionString: "host=localhost"})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if result.Output == "" {
		t.Error("getActiveConnectionsTool() output is empty — idle session should be included by default")
	}
}

func TestGetActiveConnectionsTool_ActiveOnly(t *testing.T) {
	// With ActiveOnly=true only state='active' connections are queried; idle sessions are excluded.
	mockOutput := "-[ RECORD 1 ]---+---\npid | 456\nstate | active\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{
		ConnectionString: "host=localhost",
		ActiveOnly:       true,
	})
	if err != nil {
		t.Fatalf("getActiveConnectionsTool() error = %v, want nil", err)
	}
	if !strings.Contains(result.Output, "456") {
		t.Errorf("getActiveConnectionsTool() output = %q, want to contain pid 456", result.Output)
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
// Note: Errors are now returned in the output text (not as Go errors)
// so that the LLM can see them when orchestrator calls sub-agents.
func TestToolsErrorHandling(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()
	ctx := newTestContext()

	tests := []struct {
		name     string
		fn       func() (PsqlResult, error)
		toolName string
	}{
		{"getServerInfoTool", func() (PsqlResult, error) { return getServerInfoTool(ctx, GetServerInfoArgs{}) }, "get_server_info"},
		{"getDatabaseInfoTool", func() (PsqlResult, error) { return getDatabaseInfoTool(ctx, GetDatabaseInfoArgs{}) }, "get_database_info"},
		{"getActiveConnectionsTool", func() (PsqlResult, error) { return getActiveConnectionsTool(ctx, GetActiveConnectionsArgs{}) }, "get_active_connections"},
		{"getConnectionStatsTool", func() (PsqlResult, error) { return getConnectionStatsTool(ctx, GetConnectionStatsArgs{}) }, "get_connection_stats"},
		{"getDatabaseStatsTool", func() (PsqlResult, error) { return getDatabaseStatsTool(ctx, GetDatabaseStatsArgs{}) }, "get_database_stats"},
		{"getConfigParameterTool", func() (PsqlResult, error) { return getConfigParameterTool(ctx, GetConfigParameterArgs{}) }, "get_config_parameter"},
		{"getReplicationStatusTool", func() (PsqlResult, error) { return getReplicationStatusTool(ctx, GetReplicationStatusArgs{}) }, "get_replication_status"},
		{"getLockInfoTool", func() (PsqlResult, error) { return getLockInfoTool(ctx, GetLockInfoArgs{}) }, "get_lock_info"},
		{"getTableStatsTool", func() (PsqlResult, error) { return getTableStatsTool(ctx, GetTableStatsArgs{}) }, "get_table_stats"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.fn()
			if err != nil {
				t.Errorf("%s() unexpected Go error: %v", tt.name, err)
			}
			if !strings.Contains(result.Output, "ERROR") {
				t.Errorf("%s() output = %q, want to contain 'ERROR'", tt.name, result.Output)
			}
			if !strings.Contains(result.Output, tt.toolName) {
				t.Errorf("%s() output = %q, want to contain %q", tt.name, result.Output, tt.toolName)
			}
		})
	}
}

// =============================================================================
// parseTerminatedCount
// =============================================================================

func TestParseTerminatedCount(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{
			name:   "single row terminated",
			output: "-[ RECORD 1 ]---+---\nterminated | 5\n",
			want:   5,
		},
		{
			name:   "zero terminated",
			output: "-[ RECORD 1 ]---+---\nterminated | 0\n",
			want:   0,
		},
		{
			name:   "large count",
			output: "-[ RECORD 1 ]---+---\nterminated | 42\n",
			want:   42,
		},
		{
			name:   "no terminated field",
			output: "-[ RECORD 1 ]---+---\npid | 12345\nstate | idle\n",
			want:   0,
		},
		{
			name:   "empty output",
			output: "",
			want:   0,
		},
		{
			name:   "unrelated pipe line",
			output: "name | value\n",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTerminatedCount(tt.output)
			if got != tt.want {
				t.Errorf("parseTerminatedCount(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// cancelQueryTool
// =============================================================================

func TestCancelQueryTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+------------------------------
cancelled       | t
pid             | 12345
usename         | app_user
datname         | testdb
state           | active
query_preview   | SELECT * FROM orders WHERE id = 1
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := cancelQueryTool(ctx, CancelQueryArgs{
		ConnectionString: "host=localhost",
		PID:              12345,
	})
	if err != nil {
		t.Fatalf("cancelQueryTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "cancelled") {
		t.Errorf("cancelQueryTool() output = %q, want to contain 'cancelled'", result.Output)
	}
	if !strings.Contains(result.Output, "12345") {
		t.Errorf("cancelQueryTool() output = %q, want to contain pid", result.Output)
	}
}

func TestCancelQueryTool_NoPidFound(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := cancelQueryTool(ctx, CancelQueryArgs{
		ConnectionString: "host=localhost",
		PID:              99999,
	})
	if err != nil {
		t.Fatalf("cancelQueryTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "No backend found with pid 99999") {
		t.Errorf("cancelQueryTool() output = %q, want 'No backend found' message", result.Output)
	}
}

func TestCancelQueryTool_Failure(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()

	ctx := newTestContext()
	result, err := cancelQueryTool(ctx, CancelQueryArgs{
		ConnectionString: "host=localhost",
		PID:              123,
	})
	if err != nil {
		t.Fatalf("cancelQueryTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("cancelQueryTool() output = %q, want ERROR on failure", result.Output)
	}
	if !strings.Contains(result.Output, "cancel_query") {
		t.Errorf("cancelQueryTool() output = %q, want tool name in error", result.Output)
	}
}

// =============================================================================
// terminateConnectionTool
// =============================================================================

func TestTerminateConnectionTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+------------------------------
terminated      | t
pid             | 5678
usename         | slow_client
datname         | appdb
state           | idle in transaction
query_preview   | BEGIN
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := terminateConnectionTool(ctx, TerminateConnectionArgs{
		ConnectionString: "host=localhost",
		PID:              5678,
	})
	if err != nil {
		t.Fatalf("terminateConnectionTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "terminated") {
		t.Errorf("terminateConnectionTool() output = %q, want to contain 'terminated'", result.Output)
	}
	if !strings.Contains(result.Output, "5678") {
		t.Errorf("terminateConnectionTool() output = %q, want to contain pid", result.Output)
	}
}

func TestTerminateConnectionTool_NoPidFound(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := terminateConnectionTool(ctx, TerminateConnectionArgs{
		ConnectionString: "host=localhost",
		PID:              11111,
	})
	if err != nil {
		t.Fatalf("terminateConnectionTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "No backend found with pid 11111") {
		t.Errorf("terminateConnectionTool() output = %q, want 'No backend found' message", result.Output)
	}
}

func TestTerminateConnectionTool_Failure(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()

	ctx := newTestContext()
	result, err := terminateConnectionTool(ctx, TerminateConnectionArgs{
		ConnectionString: "host=localhost",
		PID:              5678,
	})
	if err != nil {
		t.Fatalf("terminateConnectionTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("terminateConnectionTool() output = %q, want ERROR on failure", result.Output)
	}
	if !strings.Contains(result.Output, "terminate_connection") {
		t.Errorf("terminateConnectionTool() output = %q, want tool name in error", result.Output)
	}
}

// =============================================================================
// killIdleConnectionsTool
// =============================================================================

func TestKillIdleConnectionsTool_TooShortIdle(t *testing.T) {
	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      2, // Below minimum of 5
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("killIdleConnectionsTool() output = %q, want ERROR for idle_minutes < 5", result.Output)
	}
	if !strings.Contains(result.Output, "idle_minutes") {
		t.Errorf("killIdleConnectionsTool() output = %q, want idle_minutes in error", result.Output)
	}
}

func TestKillIdleConnectionsTool_DryRun_Found(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+----------
pid             | 100
usename         | app
datname         | testdb
client_addr     | 10.0.0.1
state           | idle
idle_minutes    | 15
last_query      | SELECT 1
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      10,
		DryRun:           true,
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "[DRY RUN]") {
		t.Errorf("killIdleConnectionsTool() output = %q, want [DRY RUN] prefix", result.Output)
	}
	if !strings.Contains(result.Output, "Would terminate") {
		t.Errorf("killIdleConnectionsTool() output = %q, want 'Would terminate' in dry-run output", result.Output)
	}
}

func TestKillIdleConnectionsTool_DryRun_NoneFound(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      30,
		DryRun:           true,
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "[DRY RUN]") {
		t.Errorf("killIdleConnectionsTool() output = %q, want [DRY RUN] prefix", result.Output)
	}
	if !strings.Contains(result.Output, "No idle connections") {
		t.Errorf("killIdleConnectionsTool() output = %q, want 'No idle connections'", result.Output)
	}
}

func TestKillIdleConnectionsTool_Success(t *testing.T) {
	mockOutput := "-[ RECORD 1 ]---+---\nterminated | 3\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      10,
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "terminated") {
		t.Errorf("killIdleConnectionsTool() output = %q, want terminated count", result.Output)
	}
}

func TestKillIdleConnectionsTool_WithDatabaseFilter(t *testing.T) {
	mockOutput := "-[ RECORD 1 ]---+---\nterminated | 1\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      10,
		Database:         "mydb",
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if result.Output == "" {
		t.Error("killIdleConnectionsTool() output is empty")
	}
}

func TestKillIdleConnectionsTool_Failure(t *testing.T) {
	defer withMockRunner("connection refused", errors.New("exit status 1"))()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      10,
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("killIdleConnectionsTool() output = %q, want ERROR on failure", result.Output)
	}
	if !strings.Contains(result.Output, "kill_idle_connections") {
		t.Errorf("killIdleConnectionsTool() output = %q, want tool name in error", result.Output)
	}
}

// =============================================================================
// Policy enforcement helpers for agent-package tests
// =============================================================================

// writeTempDBPolicyFile writes a YAML policy to a temp file and returns its path.
func writeTempDBPolicyFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "db-policies-*.yaml")
	if err != nil {
		t.Fatalf("create temp policy file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp policy file: %v", err)
	}
	f.Close()
	return f.Name()
}

// withPolicyEnforcer temporarily sets the package-level policyEnforcer for a test.
// Returns a cleanup function that restores the original value.
func withPolicyEnforcer(e *agentutil.PolicyEnforcer) func() {
	old := policyEnforcer
	policyEnforcer = e
	return func() { policyEnforcer = old }
}

// newDenyWriteEnforcer returns a PolicyEnforcer that denies write operations on databases.
func newDenyWriteEnforcer(t *testing.T) *agentutil.PolicyEnforcer {
	t.Helper()
	const yaml = `
version: "1"
policies:
  - name: deny-write
    resources:
      - type: database
    rules:
      - action: write
        effect: deny
        message: "write operations are not permitted in this test"
`
	path := writeTempDBPolicyFile(t, yaml)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "allow",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// newDenyDestructiveEnforcer returns a PolicyEnforcer that denies destructive operations on databases.
func newDenyDestructiveEnforcer(t *testing.T) *agentutil.PolicyEnforcer {
	t.Helper()
	const yaml = `
version: "1"
policies:
  - name: deny-destructive
    resources:
      - type: database
    rules:
      - action: destructive
        effect: deny
        message: "destructive operations are not permitted in this test"
`
	path := writeTempDBPolicyFile(t, yaml)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "allow",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// newBlastRadiusDBEnforcer returns a PolicyEnforcer that allows destructive ops
// but enforces a max_rows_affected limit (used to test kill_idle_connections blast-radius).
func newBlastRadiusDBEnforcer(t *testing.T, maxRows int) *agentutil.PolicyEnforcer {
	t.Helper()
	yamlContent := fmt.Sprintf(`
version: "1"
policies:
  - name: db-blast-radius
    resources:
      - type: database
    rules:
      - action: destructive
        effect: allow
        conditions:
          max_rows_affected: %d
`, maxRows)
	path := writeTempDBPolicyFile(t, yamlContent)
	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    path,
		DefaultPolicy: "deny",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{Engine: engine})
}

// =============================================================================
// Policy enforcement tests
// =============================================================================

func TestCancelQueryTool_PolicyDenied(t *testing.T) {
	// cancel_query uses ActionWrite; the deny-write policy should block it.
	defer withPolicyEnforcer(newDenyWriteEnforcer(t))()
	defer withMockRunner("", nil)() // should not be reached

	ctx := newTestContext()
	result, err := cancelQueryTool(ctx, CancelQueryArgs{
		ConnectionString: "host=localhost",
		PID:              123,
	})
	if err != nil {
		t.Fatalf("cancelQueryTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("cancelQueryTool() output = %q, want ERROR on policy denial", result.Output)
	}
	if !strings.Contains(result.Output, "policy denied") {
		t.Errorf("cancelQueryTool() output = %q, want 'policy denied' in error", result.Output)
	}
}

func TestTerminateConnectionTool_PolicyDenied(t *testing.T) {
	// terminate_connection uses ActionDestructive; the deny-destructive policy should block it.
	defer withPolicyEnforcer(newDenyDestructiveEnforcer(t))()
	defer withMockRunner("", nil)() // should not be reached

	ctx := newTestContext()
	result, err := terminateConnectionTool(ctx, TerminateConnectionArgs{
		ConnectionString: "host=localhost",
		PID:              5678,
	})
	if err != nil {
		t.Fatalf("terminateConnectionTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("terminateConnectionTool() output = %q, want ERROR on policy denial", result.Output)
	}
	if !strings.Contains(result.Output, "policy denied") {
		t.Errorf("terminateConnectionTool() output = %q, want 'policy denied' in error", result.Output)
	}
}

func TestKillIdleConnectionsTool_BlastRadiusDenied(t *testing.T) {
	// Policy allows destructive with max 5 rows affected.
	// Mock returns 20 terminated — exceeds the limit.
	// kill_idle_connections has an explicit secondary blast-radius check
	// using parseTerminatedCount since its output is a SELECT, not a DML tag.
	defer withPolicyEnforcer(newBlastRadiusDBEnforcer(t, 5))()
	mockOutput := "-[ RECORD 1 ]---+---\nterminated | 20\n"
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := killIdleConnectionsTool(ctx, KillIdleConnectionsArgs{
		ConnectionString: "host=localhost",
		IdleMinutes:      10,
	})
	if err != nil {
		t.Fatalf("killIdleConnectionsTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("output = %q, want ERROR when blast-radius limit (5) is exceeded (20 terminated)", result.Output)
	}
	if !strings.Contains(result.Output, "kill_idle_connections") {
		t.Errorf("output = %q, want tool name in blast-radius error", result.Output)
	}
}
