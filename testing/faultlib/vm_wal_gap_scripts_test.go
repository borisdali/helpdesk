package faultlib

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// db-pgbackrest-wal-gap-vm hides a backup's start segment in a real VM's
// repo, over SSH. Its scripts are run here against a throwaway repo and a
// fake pgbackrest, so what they move, and what they leave alone, is checked
// without a VM.

const (
	olderSeg  = "000000080000000000000019"
	latestSeg = "00000008000000000000001B"
)

// vmRepo builds a repo with two backups' start segments plus the segment
// between them, and a fake pgbackrest whose `info` reports both backups.
func vmRepo(t *testing.T, infoJSON string) (repo string, env []string, pgbackrestCalls func() []string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	dir := filepath.Join(repo, "archive", "main", "16-1", "0000000800000000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, seg := range []string{olderSeg, "00000008000000000000001A", latestSeg} {
		if err := os.WriteFile(filepath.Join(dir, seg+"-abc123.gz"), []byte(seg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "pgbackrest.calls")
	fake := "#!/bin/bash\necho \"$*\" >> " + calls + "\ncat <<'EOF'\n" + infoJSON + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(bin, "pgbackrest"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "PGBACKREST_REPO_PATH=" + repo}
	return repo, env, func() []string {
		b, _ := os.ReadFile(calls)
		return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", " \n "))
	}
}

const twoBackupsInfo = `[{"name":"main","backup":[
 {"label":"20261020F","archive":{"start":"` + olderSeg + `","stop":"` + olderSeg + `"}},
 {"label":"20261026F","archive":{"start":"` + latestSeg + `","stop":"` + latestSeg + `"}}]}]`

func runVMScript(t *testing.T, script string, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("script output:\n%s", out)
	return string(out), err
}

func repoFiles(t *testing.T, repo string) []string {
	t.Helper()
	var files []string
	_ = filepath.Walk(repo, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(repo, p)
			files = append(files, rel)
		}
		return nil
	})
	return files
}

func vmFault(t *testing.T) Failure {
	t.Helper()
	c, err := LoadBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range c.Failures {
		if f.ID == "db-pgbackrest-wal-gap-vm" {
			return f
		}
	}
	t.Fatal("db-pgbackrest-wal-gap-vm is not in the built-in catalog")
	return Failure{}
}

func hasFile(files []string, suffix string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

// The latest backup's own start segment is hidden, and nothing else moves:
// not the older backup's segment, not the one between them.
func TestVMWalGap_InjectHidesOnlyTheLatestBackupsStartSegment(t *testing.T) {
	f := vmFault(t)
	repo, env, calls := vmRepo(t, twoBackupsInfo)
	before := repoFiles(t, repo)

	if _, err := runVMScript(t, f.Inject.ScriptInline, env); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	after := repoFiles(t, repo)
	if len(after) != len(before) {
		t.Fatalf("files before %d after %d: inject must only rename", len(before), len(after))
	}
	if !hasFile(after, latestSeg+"-abc123.gz.hidden-by-fault") {
		t.Errorf("the latest backup's start segment %s was not hidden: %v", latestSeg, after)
	}
	for _, untouched := range []string{olderSeg + "-abc123.gz", "00000008000000000000001A-abc123.gz"} {
		if !hasFile(after, untouched) {
			t.Errorf("%s should be untouched: %v", untouched, after)
		}
	}
	// It reads the backup list and nothing else from pgBackRest.
	for _, c := range calls() {
		if c == "backup" || c == "restore" || c == "expire" || c == "archive-push" {
			t.Errorf("inject ran pgbackrest %s", c)
		}
	}
}

// Teardown puts it back, only what the fault hid, and can run again.
func TestVMWalGap_TeardownRestoresAndIsIdempotent(t *testing.T) {
	f := vmFault(t)
	repo, env, _ := vmRepo(t, twoBackupsInfo)
	original := repoFiles(t, repo)

	if _, err := runVMScript(t, f.Inject.ScriptInline, env); err != nil {
		t.Fatalf("inject failed: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := runVMScript(t, f.Teardown.ScriptInline, env); err != nil {
			t.Fatalf("teardown %d failed: %v", i+1, err)
		}
	}
	got := repoFiles(t, repo)
	if strings.Join(got, ",") != strings.Join(original, ",") {
		t.Errorf("repo after inject+teardown = %v, want the original %v", got, original)
	}
}

// A teardown with nothing hidden changes nothing (it also runs after an
// injection that failed part-way).
func TestVMWalGap_TeardownWithNothingHiddenIsANoOp(t *testing.T) {
	f := vmFault(t)
	repo, env, _ := vmRepo(t, twoBackupsInfo)
	original := repoFiles(t, repo)
	if _, err := runVMScript(t, f.Teardown.ScriptInline, env); err != nil {
		t.Fatalf("teardown failed: %v", err)
	}
	if got := repoFiles(t, repo); strings.Join(got, ",") != strings.Join(original, ",") {
		t.Errorf("repo changed: %v", got)
	}
}

// No backup yet: the injection fails loudly instead of silently doing nothing,
// and leaves the repo as it was.
func TestVMWalGap_InjectWithoutABackupFails(t *testing.T) {
	f := vmFault(t)
	repo, env, _ := vmRepo(t, `[{"name":"main","backup":[]}]`)
	original := repoFiles(t, repo)
	out, err := runVMScript(t, f.Inject.ScriptInline, env)
	if err == nil {
		t.Fatal("inject reported success with no backup to hide a segment of")
	}
	if !strings.Contains(out, "no backup found") {
		t.Errorf("the failure does not say why: %q", out)
	}
	if got := repoFiles(t, repo); strings.Join(got, ",") != strings.Join(original, ",") {
		t.Errorf("repo changed: %v", got)
	}
}

// writeExecutingSSHOnPath is a fake ssh that records its argv and then runs the
// script it is sent on stdin with `bash -s`, as the remote end would. Unlike
// writeFakeSSHOnPath it lets a fault's real script run, so the whole injector
// path is exercised: the arguments, the preamble the injector prepends, and the
// script arriving on stdin.
func writeExecutingSSHOnPath(t *testing.T) (capturePath string) {
	t.Helper()
	dir := t.TempDir()
	capturePath = filepath.Join(dir, "captured_args")
	script := "#!/bin/bash\necho \"$*\" >> " + capturePath + "\nexec bash -s\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return capturePath
}

// The fault as the harness really runs it: through Injector.Inject/Teardown,
// ssh options and all, the script piped to a remote `bash -s`.
func TestVMWalGap_ThroughTheInjectorOverSSH(t *testing.T) {
	f := vmFault(t)
	repo, env, _ := vmRepo(t, twoBackupsInfo)
	for _, kv := range env { // the fake ssh runs locally and inherits these
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	capture := writeExecutingSSHOnPath(t)
	original := repoFiles(t, repo)

	inj := NewInjector(&HarnessConfig{SSHHost: "postgres@10.0.0.9", SSHKeyPath: "/etc/faulttest-ssh/id_ed25519", SSHPort: 2222})
	if err := inj.Inject(context.Background(), f); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !hasFile(repoFiles(t, repo), latestSeg+"-abc123.gz.hidden-by-fault") {
		t.Fatalf("the latest start segment was not hidden through the injector: %v", repoFiles(t, repo))
	}
	if err := inj.Teardown(context.Background(), f); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if got := repoFiles(t, repo); strings.Join(got, ",") != strings.Join(original, ",") {
		t.Errorf("repo after inject+teardown = %v, want %v", got, original)
	}

	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		for _, want := range []string{"-o StrictHostKeyChecking=no", "-o BatchMode=yes", "-i /etc/faulttest-ssh/id_ed25519", "-p 2222", "postgres@10.0.0.9", "bash -s"} {
			if !strings.Contains(line, want) {
				t.Errorf("ssh invocation %q lacks %q", line, want)
			}
		}
	}
	if n := len(strings.Split(strings.TrimSpace(string(b)), "\n")); n != 2 {
		t.Errorf("ssh was invoked %d times, want 2 (inject, teardown)", n)
	}
}
