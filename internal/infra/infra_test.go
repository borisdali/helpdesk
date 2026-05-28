package infra

import (
	"os"
	"testing"
)

func TestResolvedConnectionString_NoPasswordEnv(t *testing.T) {
	db := DBServer{ConnectionString: "host=localhost dbname=mydb user=postgres"}
	got := db.ResolvedConnectionString()
	if got != db.ConnectionString {
		t.Errorf("got %q, want %q", got, db.ConnectionString)
	}
}

func TestResolvedConnectionString_EnvPresent(t *testing.T) {
	t.Setenv("TEST_INFRA_PW", "secret123")
	db := DBServer{
		ConnectionString: "host=localhost dbname=mydb user=postgres",
		PasswordEnv:      "TEST_INFRA_PW",
	}
	want := "host=localhost dbname=mydb user=postgres password=secret123"
	got := db.ResolvedConnectionString()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolvedConnectionString_EnvAbsent(t *testing.T) {
	os.Unsetenv("TEST_INFRA_PW_MISSING")
	db := DBServer{
		ConnectionString: "host=localhost dbname=mydb user=postgres",
		PasswordEnv:      "TEST_INFRA_PW_MISSING",
	}
	got := db.ResolvedConnectionString()
	if got != db.ConnectionString {
		t.Errorf("env absent: got %q, want base string %q", got, db.ConnectionString)
	}
}

func TestResolvedConnectionString_EnvEmpty(t *testing.T) {
	t.Setenv("TEST_INFRA_PW_EMPTY", "")
	db := DBServer{
		ConnectionString: "host=localhost dbname=mydb user=postgres",
		PasswordEnv:      "TEST_INFRA_PW_EMPTY",
	}
	got := db.ResolvedConnectionString()
	if got != db.ConnectionString {
		t.Errorf("env empty: got %q, want base string %q", got, db.ConnectionString)
	}
}

func TestFindDBByConnStr(t *testing.T) {
	cfg := &Config{
		DBServers: map[string]DBServer{
			"prod-db": {
				Name:                  "Production DB",
				ConnectionString:      "host=prod.internal port=5432 dbname=app user=app",
				ApprovalOverrideRoles: []string{"dba_lead"},
			},
			"dev-db": {
				Name:             "Dev DB",
				ConnectionString: "host=localhost port=5432 dbname=devdb user=postgres",
			},
		},
	}

	tests := []struct {
		name      string
		input     string
		wantKey   string
		wantFound bool
	}{
		{"by config key", "prod-db", "prod-db", true},
		{"by display name", "Production DB", "prod-db", true},
		{"by full connection string", "host=prod.internal port=5432 dbname=app user=app", "prod-db", true},
		{"by endpoint match (with extra fields)", "host=prod.internal port=5432 dbname=app user=other password=x", "prod-db", true},
		{"not found", "host=unknown port=9999 dbname=nope", "", false},
		{"empty input", "", "", false},
		{"nil config", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			if tt.name == "nil config" {
				c = nil
			}
			db, key, ok := c.FindDBByConnStr(tt.input)
			if ok != tt.wantFound {
				t.Fatalf("found=%v, want %v", ok, tt.wantFound)
			}
			if ok {
				if key != tt.wantKey {
					t.Errorf("key=%q, want %q", key, tt.wantKey)
				}
				if db == nil {
					t.Error("db is nil on found=true")
				}
			}
		})
	}
}
