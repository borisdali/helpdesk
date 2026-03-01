package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"helpdesk/internal/audit"
	"helpdesk/internal/infra"
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
// parsePgFunctionResult
// =============================================================================

func TestParsePgFunctionResult(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{
			// Typical psql -x output when pg_cancel_backend succeeds.
			name: "cancelled true",
			output: `-[ RECORD 1 ]---+------------------------------
cancelled       | t
pid             | 12345
usename         | app_user
datname         | testdb
state           | active
query_preview   | SELECT 1
`,
			want: 1,
		},
		{
			// pg_cancel_backend returned false (backend existed but cancel failed).
			name: "cancelled false",
			output: `-[ RECORD 1 ]---+------------------------------
cancelled       | f
pid             | 12345
usename         | app_user
datname         | testdb
state           | active
query_preview   | SELECT 1
`,
			want: 0,
		},
		{
			// Typical psql -x output when pg_terminate_backend succeeds.
			name: "terminated true",
			output: `-[ RECORD 1 ]---+------------------------------
terminated      | t
pid             | 5678
usename         | other_user
datname         | proddb
state           | idle
query_preview   | COMMIT
`,
			want: 1,
		},
		{
			// pg_terminate_backend returned false.
			name: "terminated false",
			output: `-[ RECORD 1 ]---+------------------------------
terminated      | f
pid             | 5678
usename         | other_user
datname         | proddb
state           | idle
query_preview   | COMMIT
`,
			want: 0,
		},
		{
			// No matching column — should return 0.
			name:   "no relevant column",
			output: "-[ RECORD 1 ]---+---\npid | 12345\nstate | idle\n",
			want:   0,
		},
		{
			name:   "empty output",
			output: "",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePgFunctionResult(tt.output)
			if got != tt.want {
				t.Errorf("parsePgFunctionResult(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// parseExpandedRow / parseConnectionPlan / formatDuration / formatConnectionPlan
// =============================================================================

func TestParseExpandedRow(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   map[string]string
	}{
		{
			name: "simple record",
			input: `-[ RECORD 1 ]---+----
pid             | 12345
usename         | alice
datname         | mydb
state           | active
`,
			want: map[string]string{
				"pid":     "12345",
				"usename": "alice",
				"datname": "mydb",
				"state":   "active",
			},
		},
		{
			name: "boolean fields",
			input: `-[ RECORD 1 ]---+----
has_open_tx     | t
has_writes      | f
`,
			want: map[string]string{
				"has_open_tx": "t",
				"has_writes":  "f",
			},
		},
		{
			name: "empty value",
			input: `-[ RECORD 1 ]---+----
locked_tables   |
current_query   | SELECT 1
`,
			want: map[string]string{
				"locked_tables": "",
				"current_query": "SELECT 1",
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseExpandedRow(tt.input)
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("parseExpandedRow()[%q] = %q, want %q", k, got[k], want)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseExpandedRow() returned %d keys, want %d; got %v", len(got), len(tt.want), got)
			}
		})
	}
}

func TestParseConnectionPlan(t *testing.T) {
	t.Run("idle session no tx", func(t *testing.T) {
		output := `-[ RECORD 1 ]---+----
pid                  | 111
usename              | alice
datname              | mydb
client_addr          | 10.0.0.1
state                | idle
state_duration_secs  | 30
has_open_tx          | f
open_tx_secs         | 0
has_writes           | f
total_locks          | 2
row_locks            | 0
locked_tables        | pg_class
current_query        | SELECT 1
`
		plan := parseConnectionPlan(111, output)
		if plan.PID != 111 {
			t.Errorf("PID = %d, want 111", plan.PID)
		}
		if plan.User != "alice" {
			t.Errorf("User = %q, want alice", plan.User)
		}
		if plan.State != "idle" {
			t.Errorf("State = %q, want idle", plan.State)
		}
		if plan.StateDurationSecs != 30 {
			t.Errorf("StateDurationSecs = %d, want 30", plan.StateDurationSecs)
		}
		if plan.HasOpenTransaction {
			t.Error("HasOpenTransaction = true, want false")
		}
		if plan.HasWrites {
			t.Error("HasWrites = true, want false")
		}
		if plan.RollbackMinSecs != 0 || plan.RollbackMaxSecs != 0 {
			t.Errorf("rollback estimate = [%d, %d], want [0, 0] for read-only", plan.RollbackMinSecs, plan.RollbackMaxSecs)
		}
	})

	t.Run("write tx with locks", func(t *testing.T) {
		output := `-[ RECORD 1 ]---+----
pid                  | 222
usename              | writer
datname              | proddb
client_addr          | 192.168.1.5
state                | idle in transaction
state_duration_secs  | 120
has_open_tx          | t
open_tx_secs         | 600
has_writes           | t
total_locks          | 5
row_locks            | 2
locked_tables        | orders, customers
current_query        | UPDATE orders SET status = 'shipped' WHERE id = 42
`
		plan := parseConnectionPlan(222, output)
		if !plan.HasOpenTransaction {
			t.Error("HasOpenTransaction = false, want true")
		}
		if plan.OpenTxAgeSecs != 600 {
			t.Errorf("OpenTxAgeSecs = %d, want 600", plan.OpenTxAgeSecs)
		}
		if !plan.HasWrites {
			t.Error("HasWrites = false, want true")
		}
		if len(plan.LockedTables) != 2 {
			t.Errorf("LockedTables = %v, want [orders customers]", plan.LockedTables)
		}
		if plan.TotalLocks != 5 {
			t.Errorf("TotalLocks = %d, want 5", plan.TotalLocks)
		}
		if plan.RowLocks != 2 {
			t.Errorf("RowLocks = %d, want 2", plan.RowLocks)
		}
		// 600 seconds: min = max(1, 300) = 300, max = 1200
		if plan.RollbackMinSecs != 300 {
			t.Errorf("RollbackMinSecs = %d, want 300", plan.RollbackMinSecs)
		}
		if plan.RollbackMaxSecs != 1200 {
			t.Errorf("RollbackMaxSecs = %d, want 1200", plan.RollbackMaxSecs)
		}
	})

	t.Run("short write tx rollback minimum", func(t *testing.T) {
		// open_tx_secs=1 → min = max(1, 0) = 1, max = 2
		output := `-[ RECORD 1 ]---+----
pid                  | 333
usename              | u
datname              | db
client_addr          | local
state                | idle in transaction
state_duration_secs  | 0
has_open_tx          | t
open_tx_secs         | 1
has_writes           | t
total_locks          | 1
row_locks            | 0
locked_tables        |
current_query        | INSERT INTO t VALUES (1)
`
		plan := parseConnectionPlan(333, output)
		if plan.RollbackMinSecs != 1 {
			t.Errorf("RollbackMinSecs = %d, want 1 (clamped minimum)", plan.RollbackMinSecs)
		}
		if plan.RollbackMaxSecs != 2 {
			t.Errorf("RollbackMaxSecs = %d, want 2", plan.RollbackMaxSecs)
		}
	})
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		secs int
		want string
	}{
		{0, "0s"},
		{-5, "0s"},
		{1, "1s"},
		{59, "59s"},
		{60, "1m 0s"},
		{90, "1m 30s"},
		{3599, "59m 59s"},
		{3600, "1h 0m"},
		{3661, "1h 1m"},
		{7322, "2h 2m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.secs)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}

func TestFormatConnectionPlan(t *testing.T) {
	t.Run("no open transaction", func(t *testing.T) {
		plan := ConnectionPlan{
			PID:        42,
			User:       "alice",
			Database:   "mydb",
			ClientAddr: "10.0.0.1",
			State:      "idle",
			StateDurationSecs: 15,
		}
		out := formatConnectionPlan(plan)
		if !strings.Contains(out, "Session PID 42") {
			t.Errorf("missing PID header: %q", out)
		}
		if !strings.Contains(out, "No open transaction") {
			t.Errorf("missing no-tx message: %q", out)
		}
		if strings.Contains(out, "Transaction:") {
			t.Errorf("should not contain Transaction section: %q", out)
		}
	})

	t.Run("read-only transaction", func(t *testing.T) {
		plan := ConnectionPlan{
			PID:                42,
			User:               "bob",
			Database:           "proddb",
			State:              "idle in transaction",
			HasOpenTransaction: true,
			OpenTxAgeSecs:      300,
			HasWrites:          false,
		}
		out := formatConnectionPlan(plan)
		if !strings.Contains(out, "Transaction:") {
			t.Errorf("missing Transaction section: %q", out)
		}
		if !strings.Contains(out, "read-only") {
			t.Errorf("missing read-only label: %q", out)
		}
		if strings.Contains(out, "Rollback estimate") {
			t.Errorf("should not have rollback estimate for read-only: %q", out)
		}
	})

	t.Run("write transaction with estimate", func(t *testing.T) {
		plan := ConnectionPlan{
			PID:                99,
			User:               "writer",
			Database:           "orders",
			State:              "idle in transaction",
			HasOpenTransaction: true,
			OpenTxAgeSecs:      600,
			HasWrites:          true,
			TotalLocks:         3,
			RowLocks:           1,
			LockedTables:       []string{"orders"},
			RollbackMinSecs:    300,
			RollbackMaxSecs:    1200,
			CurrentQuery:       "UPDATE orders SET x=1",
		}
		out := formatConnectionPlan(plan)
		if !strings.Contains(out, "Has writes:    yes") {
			t.Errorf("missing has-writes: %q", out)
		}
		if !strings.Contains(out, "Locked tables: orders") {
			t.Errorf("missing locked tables: %q", out)
		}
		if !strings.Contains(out, "Rollback estimate") {
			t.Errorf("missing rollback estimate: %q", out)
		}
		if !strings.Contains(out, "UPDATE orders") {
			t.Errorf("missing last query: %q", out)
		}
	})
}

// =============================================================================
// getSessionInfoTool
// =============================================================================

func TestGetSessionInfoTool_Success(t *testing.T) {
	mockOutput := `-[ RECORD 1 ]---+----
pid                  | 38553
usename              | app_user
datname              | production
client_addr          | 10.1.2.3
state                | idle in transaction
state_duration_secs  | 45
has_open_tx          | t
open_tx_secs         | 45
has_writes           | t
total_locks          | 3
row_locks            | 1
locked_tables        | orders
current_query        | UPDATE orders SET shipped = true WHERE id = 7
`
	defer withMockRunner(mockOutput, nil)()

	ctx := newTestContext()
	result, err := getSessionInfoTool(ctx, GetSessionInfoArgs{
		ConnectionString: "host=localhost",
		PID:              38553,
	})
	if err != nil {
		t.Fatalf("getSessionInfoTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "Session PID 38553") {
		t.Errorf("output missing PID header: %q", result.Output)
	}
	if !strings.Contains(result.Output, "app_user") {
		t.Errorf("output missing user: %q", result.Output)
	}
	if !strings.Contains(result.Output, "Has writes:    yes") {
		t.Errorf("output missing has-writes: %q", result.Output)
	}
	if !strings.Contains(result.Output, "Rollback estimate") {
		t.Errorf("output missing rollback estimate: %q", result.Output)
	}
}

func TestGetSessionInfoTool_NoPidFound(t *testing.T) {
	defer withMockRunner("(0 rows)", nil)()

	ctx := newTestContext()
	result, err := getSessionInfoTool(ctx, GetSessionInfoArgs{
		ConnectionString: "host=localhost",
		PID:              9999,
	})
	if err != nil {
		t.Fatalf("getSessionInfoTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "no session found with pid 9999") {
		t.Errorf("output = %q, want 'no session found' message", result.Output)
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
	// inspectConnection is now called first; "(0 rows)" from the mock causes it to
	// report "no session found" before the actual cancel runs.
	if !strings.Contains(result.Output, "no session found with pid 99999") {
		t.Errorf("cancelQueryTool() output = %q, want 'no session found' message", result.Output)
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
	// inspectConnection is now called first; "(0 rows)" from the mock causes it to
	// report "no session found" before the actual terminate runs.
	if !strings.Contains(result.Output, "no session found with pid 11111") {
		t.Errorf("terminateConnectionTool() output = %q, want 'no session found' message", result.Output)
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

func TestCancelQueryTool_PostExecPolicyChecked(t *testing.T) {
	// Verifies that when policyEnforcer is set and pg_cancel_backend returns "t",
	// the explicit post-exec CheckDatabaseResult block is exercised.
	// cancel_query targets a single backend — blast-radius ceiling is always 1,
	// so this test focuses on exercising the code path (not triggering denial).
	defer withPolicyEnforcer(newDenyDestructiveEnforcer(t))() // allows write, denies destructive
	mockOutput := `-[ RECORD 1 ]---+------------------------------
cancelled       | t
pid             | 12345
usename         | app_user
datname         | testdb
state           | active
query_preview   | SELECT 1
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
	// Write action is allowed by newDenyDestructiveEnforcer (DefaultPolicy: allow).
	// Post-exec block runs with RowsAffected=1; policy allows write → no error.
	if strings.Contains(result.Output, "ERROR") {
		t.Errorf("cancelQueryTool() output = %q, want no error when write is allowed", result.Output)
	}
}

func TestTerminateConnectionTool_PostExecPolicyChecked(t *testing.T) {
	// Verifies that when policyEnforcer is set and pg_terminate_backend returns "t",
	// the explicit post-exec CheckDatabaseResult block is exercised.
	// terminate_connection targets a single backend — blast-radius ceiling is always 1,
	// so this test focuses on exercising the code path (not triggering denial).
	defer withPolicyEnforcer(newDenyWriteEnforcer(t))() // allows destructive, denies write
	mockOutput := `-[ RECORD 1 ]---+------------------------------
terminated      | t
pid             | 5678
usename         | app_user
datname         | testdb
state           | idle
query_preview   | COMMIT
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
	// Destructive action is allowed by newDenyWriteEnforcer (DefaultPolicy: allow).
	// Post-exec block runs with RowsAffected=1; policy allows destructive → no error.
	if strings.Contains(result.Output, "ERROR") {
		t.Errorf("terminateConnectionTool() output = %q, want no error when destructive is allowed", result.Output)
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

// =============================================================================
// Infra.json enforcement tests
// =============================================================================

// withInfraConfig temporarily sets the package-level infraConfig for a test.
func withInfraConfig(cfg *infra.Config) func() {
	old := infraConfig
	infraConfig = cfg
	return func() { infraConfig = old }
}

// makeTestInfraConfig builds a minimal infra.Config with one registered database.
func makeTestInfraConfig() *infra.Config {
	return &infra.Config{
		DBServers: map[string]infra.DBServer{
			"prod-db": {
				Name:             "prod-db",
				ConnectionString: "host=prod.example.com dbname=mydb user=postgres",
				Tags:             []string{"production"},
			},
		},
	}
}

func TestResolveDatabaseInfo_InfraEnforced_UnknownConnString(t *testing.T) {
	// infraConfig is set but the connection string is not registered → hard reject.
	defer withInfraConfig(makeTestInfraConfig())()

	_, err := resolveDatabaseInfo("host=unknown.example.com dbname=other user=postgres")
	if err == nil {
		t.Fatal("resolveDatabaseInfo() error = nil, want error for unregistered conn string with infra config set")
	}
	if !strings.Contains(err.Error(), "not registered in infrastructure config") {
		t.Errorf("resolveDatabaseInfo() error = %q, want 'not registered in infrastructure config'", err.Error())
	}
	if !strings.Contains(err.Error(), "prod-db") {
		t.Errorf("resolveDatabaseInfo() error = %q, want known database 'prod-db' listed", err.Error())
	}
}

func TestResolveDatabaseInfo_InfraEnforced_UnknownName(t *testing.T) {
	// infraConfig is set but the database name is not registered → hard reject.
	defer withInfraConfig(makeTestInfraConfig())()

	_, err := resolveDatabaseInfo("unknown-db")
	if err == nil {
		t.Fatal("resolveDatabaseInfo() error = nil, want error for unregistered name with infra config set")
	}
	if !strings.Contains(err.Error(), "not registered in infrastructure config") {
		t.Errorf("resolveDatabaseInfo() error = %q, want 'not registered in infrastructure config'", err.Error())
	}
	if !strings.Contains(err.Error(), "prod-db") {
		t.Errorf("resolveDatabaseInfo() error = %q, want known database 'prod-db' listed", err.Error())
	}
}

func TestResolveDatabaseInfo_InfraEnforced_RegisteredConnString(t *testing.T) {
	// infraConfig is set and the connection string is registered → succeed with tags.
	defer withInfraConfig(makeTestInfraConfig())()

	info, err := resolveDatabaseInfo("host=prod.example.com dbname=mydb user=postgres")
	if err != nil {
		t.Fatalf("resolveDatabaseInfo() error = %v, want nil for registered conn string", err)
	}
	if info.Name != "prod-db" {
		t.Errorf("resolveDatabaseInfo() Name = %q, want 'prod-db'", info.Name)
	}
	if len(info.Tags) == 0 || info.Tags[0] != "production" {
		t.Errorf("resolveDatabaseInfo() Tags = %v, want ['production']", info.Tags)
	}
}

func TestResolveDatabaseInfo_InfraPermissive_UnknownConnString(t *testing.T) {
	// infraConfig is nil (dev mode) → unregistered conn string is allowed with no tags.
	defer withInfraConfig(nil)()

	info, err := resolveDatabaseInfo("host=localhost dbname=devdb user=dev")
	if err != nil {
		t.Fatalf("resolveDatabaseInfo() error = %v, want nil in dev mode (no infra config)", err)
	}
	if info.Tags != nil {
		t.Errorf("resolveDatabaseInfo() Tags = %v, want nil in dev mode", info.Tags)
	}
}

func TestCheckConnectionTool_InfraEnforced_Rejected(t *testing.T) {
	// infraConfig is set but connection string is not registered → tool returns ERROR.
	defer withInfraConfig(makeTestInfraConfig())()
	defer withMockRunner("", nil)() // should not be reached

	ctx := newTestContext()
	result, err := checkConnectionTool(ctx, CheckConnectionArgs{
		ConnectionString: "host=unknown.example.com dbname=other user=postgres",
	})
	if err != nil {
		t.Fatalf("checkConnectionTool() unexpected Go error: %v", err)
	}
	if !strings.Contains(result.Output, "ERROR") {
		t.Errorf("checkConnectionTool() output = %q, want ERROR for unregistered database", result.Output)
	}
	if !strings.Contains(result.Output, "not registered in infrastructure config") {
		t.Errorf("checkConnectionTool() output = %q, want infra rejection message", result.Output)
	}
}

// =============================================================================
// Session plan forwarding to approval context
// =============================================================================

// multiMockRunner returns successive outputs for each call to Run.
// Useful when a tool makes multiple psql calls (e.g. inspectConnection
// followed by the action query).
type multiMockRunner struct {
	outputs []string
	call    int
}

func (m *multiMockRunner) Run(_ context.Context, _ string, _ []string, _ []string) (string, error) {
	i := m.call
	if i >= len(m.outputs) {
		i = len(m.outputs) - 1
	}
	m.call++
	return m.outputs[i], nil
}

// withMultiRunner temporarily replaces cmdRunner with a multiMockRunner that
// cycles through the given outputs. Returns a cleanup func.
func withMultiRunner(outputs ...string) func() {
	r := &multiMockRunner{outputs: outputs}
	old := cmdRunner
	cmdRunner = r
	return func() { cmdRunner = old }
}

// mockApprovalServerForTools starts a minimal approval API mock.
// It captures POST /v1/approvals bodies and immediately approves all requests.
func mockApprovalServerForTools(t *testing.T) (string, <-chan audit.ApprovalCreateRequest) {
	t.Helper()
	ch := make(chan audit.ApprovalCreateRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/approvals":
			var req audit.ApprovalCreateRequest
			json.NewDecoder(r.Body).Decode(&req)
			ch <- req
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(audit.ApprovalCreateResponse{
				ApprovalID: "tool-approval-1",
				Status:     "pending",
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/wait"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(audit.StoredApproval{
				ApprovalID: "tool-approval-1",
				Status:     "approved",
				ResolvedBy: "auto-approver",
			})
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ch
}

const requireApprovalPolicy = `
version: "1"
policies:
  - name: require-approval-policy
    resources:
      - type: database
    rules:
      - action: read
        effect: allow
      - action: write
        effect: require_approval
      - action: destructive
        effect: require_approval
`

// newToolTestEnforcer creates a PolicyEnforcer that requires approval for
// write and destructive database actions, pointing at the given approval server.
func newToolTestEnforcer(t *testing.T, approvalURL string) *agentutil.PolicyEnforcer {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "policy-*.yaml")
	if err != nil {
		t.Fatalf("create temp policy file: %v", err)
	}
	if _, err := f.WriteString(requireApprovalPolicy); err != nil {
		t.Fatalf("write temp policy file: %v", err)
	}
	f.Close()

	engine, err := agentutil.InitPolicyEngine(agentutil.Config{
		PolicyEnabled: true,
		PolicyFile:    f.Name(),
		DefaultPolicy: "deny",
	})
	if err != nil {
		t.Fatalf("InitPolicyEngine: %v", err)
	}
	return agentutil.NewPolicyEnforcerWithConfig(agentutil.PolicyEnforcerConfig{
		Engine:         engine,
		ApprovalClient: audit.NewApprovalClient(approvalURL),
	})
}

// sessionInfoMockOutput is a realistic psql expanded-format response that
// inspectConnection can parse via parseConnectionPlan.
const sessionInfoMockOutput = `-[ RECORD 1 ]---+------------------------------
usename         | app_user
datname         | appdb
client_addr     | 10.0.0.5
state           | active
state_duration_secs | 300
has_open_tx     | f
open_tx_secs    | 0
has_writes      | f
total_locks     | 0
row_locks       | 0
locked_tables   |
current_query   | SELECT * FROM orders LIMIT 10`

func TestCancelQueryTool_SessionPlanSentToPolicy(t *testing.T) {
	appURL, captured := mockApprovalServerForTools(t)
	defer withPolicyEnforcer(newToolTestEnforcer(t, appURL))()

	cancelOutput := `-[ RECORD 1 ]---+------------------------------
cancelled       | t
pid             | 5678
usename         | app_user
datname         | appdb
state           | active
query_preview   | SELECT * FROM orders LIMIT 10
`
	defer withMultiRunner(sessionInfoMockOutput, cancelOutput)()

	ctx := newTestContext()
	result, err := cancelQueryTool(ctx, CancelQueryArgs{
		ConnectionString: "host=localhost",
		PID:              5678,
	})
	if err != nil {
		t.Fatalf("cancelQueryTool() unexpected Go error: %v", err)
	}
	if strings.Contains(result.Output, "ERROR") {
		t.Fatalf("cancelQueryTool() returned error output: %s", result.Output)
	}

	// The pre-execution approval request must carry the session plan.
	select {
	case req := <-captured:
		if req.Context == nil {
			t.Fatal("approval request_context is nil; cancelQueryTool must pass session plan to policy check")
		}
		si, ok := req.Context["session_info"]
		if !ok {
			t.Fatalf("approval request_context missing 'session_info'; got: %v", req.Context)
		}
		sessionPlan := fmt.Sprintf("%v", si)
		for _, want := range []string{"5678", "app_user", "active"} {
			if !strings.Contains(sessionPlan, want) {
				t.Errorf("session_info missing %q; got: %s", want, sessionPlan)
			}
		}
	default:
		t.Fatal("no approval request captured; policy enforcer was not called or did not require approval")
	}
}

func TestTerminateConnectionTool_SessionPlanSentToPolicy(t *testing.T) {
	appURL, captured := mockApprovalServerForTools(t)
	defer withPolicyEnforcer(newToolTestEnforcer(t, appURL))()

	terminateOutput := `-[ RECORD 1 ]---+------------------------------
terminated      | t
pid             | 7890
usename         | app_user
datname         | appdb
state           | active
query_preview   | UPDATE accounts SET balance = 0
`
	defer withMultiRunner(sessionInfoMockOutput, terminateOutput)()

	ctx := newTestContext()
	result, err := terminateConnectionTool(ctx, TerminateConnectionArgs{
		ConnectionString: "host=localhost",
		PID:              7890,
	})
	if err != nil {
		t.Fatalf("terminateConnectionTool() unexpected Go error: %v", err)
	}
	if strings.Contains(result.Output, "ERROR") {
		t.Fatalf("terminateConnectionTool() returned error output: %s", result.Output)
	}

	// The pre-execution approval request must carry the session plan.
	select {
	case req := <-captured:
		if req.Context == nil {
			t.Fatal("approval request_context is nil; terminateConnectionTool must pass session plan to policy check")
		}
		si, ok := req.Context["session_info"]
		if !ok {
			t.Fatalf("approval request_context missing 'session_info'; got: %v", req.Context)
		}
		sessionPlan := fmt.Sprintf("%v", si)
		// PID comes from the args (7890), user/state come from inspectConnection mock output.
		for _, want := range []string{"7890", "app_user", "active"} {
			if !strings.Contains(sessionPlan, want) {
				t.Errorf("session_info missing %q; got: %s", want, sessionPlan)
			}
		}
	default:
		t.Fatal("no approval request captured; policy enforcer was not called or did not require approval")
	}
}
