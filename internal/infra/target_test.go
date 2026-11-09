package infra

import (
	"errors"
	"strings"
	"testing"
)

func targetFixture() *Config {
	return &Config{DBServers: map[string]DBServer{
		"prod-main":   {Name: "Prod Main", ConnectionString: "host=db1.example.com port=5432 dbname=app user=u password=s3cret"},
		"prod-other":  {Name: "Prod Other", ConnectionString: "host=db2.example.com port=5432 dbname=app user=u"},
		"vm-pg":       {Name: "VM", ConnectionString: "host=pgbackrest-vm.test port=5433 dbname=postgres"},
		"docker-pg":   {Name: "Docker", ConnectionString: "host=localhost port=15432 dbname=postgres", ContainerName: "pg-fixture"},
		"noport":      {Name: "No Port", ConnectionString: "host=noport.example.com dbname=postgres"},
		"dup-a":       {Name: "Twin", ConnectionString: "host=twin-a port=6000"},
		"dup-b":       {Name: "Twin", ConnectionString: "host=twin-b port=6001"},
		"cont-a":      {ConnectionString: "host=ca port=7000", ContainerName: "shared-name"},
		"cont-b":      {ConnectionString: "host=cb port=7001", ContainerName: "shared-name"},
		"only-5433-x": {ConnectionString: "host=other port=5434"},
	}}
}

func TestResolveTarget(t *testing.T) {
	cfg := targetFixture()
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"the key", "prod-main", "prod-main"},
		{"the key with surrounding space", "  prod-main \n", "prod-main"},
		{"the full DSN, password and all", "host=db1.example.com port=5432 dbname=app user=u password=s3cret", "prod-main"},
		{"a DSN with another password: credentials play no part", "host=db1.example.com port=5432 dbname=app user=other password=different", "prod-main"},
		{"a DSN with no credentials", "host=db1.example.com port=5432 dbname=app", "prod-main"},
		{"a URI", "postgres://u:pw@db2.example.com:5432/app", "prod-other"},
		{"host:port", "pgbackrest-vm.test:5433", "vm-pg"},
		{"a bare port that only one server has", "15432", "docker-pg"},
		{"the container name", "pg-fixture", "docker-pg"},
		{"the display name", "Prod Other", "prod-other"},
		{"a full connection string with no port", "host=noport.example.com dbname=postgres", "noport"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cfg.ResolveTarget(tc.target)
			if err != nil || got != tc.want {
				t.Errorf("ResolveTarget(%q) = %q, %v; want %q", tc.target, got, err, tc.want)
			}
		})
	}
}

// The same port on different hosts must not be mistaken for one another: 5432
// is Postgres's default and collides across unrelated servers. A DSN names its
// host, so it resolves; a bare port does not, so it is refused.
func TestResolveTarget_SamePortDifferentHosts(t *testing.T) {
	cfg := targetFixture()
	if got, err := cfg.ResolveTarget("host=db2.example.com port=5432"); err != nil || got != "prod-other" {
		t.Errorf("host db2 port 5432 = %q, %v; want prod-other", got, err)
	}
	_, err := cfg.ResolveTarget("5432")
	var amb *AmbiguousTargetError
	if !errors.As(err, &amb) {
		t.Fatalf("a bare 5432 should be ambiguous, got %v", err)
	}
	if amb.By != "host+port" || strings.Join(amb.Matches, ",") != "prod-main,prod-other" {
		t.Errorf("ambiguity = %+v", amb)
	}
	if !strings.Contains(err.Error(), "refusing to guess") {
		t.Errorf("message = %q", err)
	}
}

func TestResolveTarget_AmbiguousNames(t *testing.T) {
	cfg := targetFixture()
	for _, tc := range []struct{ target, by string }{
		{"Twin", "name"},
		{"shared-name", "name"},
	} {
		_, err := cfg.ResolveTarget(tc.target)
		var amb *AmbiguousTargetError
		if !errors.As(err, &amb) || amb.By != tc.by {
			t.Errorf("ResolveTarget(%q) = %v, want an ambiguity by %s", tc.target, err, tc.by)
		}
	}
}

func TestResolveTarget_NotFound(t *testing.T) {
	cfg := targetFixture()
	for _, target := range []string{"", "   ", "nope", "host=unknown port=1", "9999", "postgres://u:p@elsewhere:5432/x"} {
		if got, err := cfg.ResolveTarget(target); !errors.Is(err, ErrTargetNotFound) || got != "" {
			t.Errorf("ResolveTarget(%q) = %q, %v; want ErrTargetNotFound", target, got, err)
		}
	}
	var nilCfg *Config
	if _, err := nilCfg.ResolveTarget("prod-main"); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("nil config: %v", err)
	}
	if _, err := (&Config{}).ResolveTarget("prod-main"); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("empty config: %v", err)
	}
}

// Resolution is deterministic: Go randomises map iteration, and a target that
// can only match one server must never come back as another.
func TestResolveTarget_IsDeterministic(t *testing.T) {
	cfg := targetFixture()
	for i := 0; i < 200; i++ {
		if got, err := cfg.ResolveTarget("host=pgbackrest-vm.test port=5433 dbname=postgres"); err != nil || got != "vm-pg" {
			t.Fatalf("run %d: %q, %v", i, got, err)
		}
		_, err := cfg.ResolveTarget("5432")
		var amb *AmbiguousTargetError
		if !errors.As(err, &amb) || strings.Join(amb.Matches, ",") != "prod-main,prod-other" {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

func TestConnStrHostPort(t *testing.T) {
	tests := []struct{ in, host, port string }{
		{"host=h port=1 dbname=d", "h", "1"},
		{"postgres://u:p@h:2/d", "h", "2"},
		{"postgresql://h:3", "h", "3"},
		{"h:4", "h", "4"},
		{"58989", "", "58989"},
		{"host=only", "only", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := ConnStrHost(tc.in); got != tc.host {
			t.Errorf("ConnStrHost(%q) = %q, want %q", tc.in, got, tc.host)
		}
		if got := ConnStrPort(tc.in); got != tc.port {
			t.Errorf("ConnStrPort(%q) = %q, want %q", tc.in, got, tc.port)
		}
	}
}
