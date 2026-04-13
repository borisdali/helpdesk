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
