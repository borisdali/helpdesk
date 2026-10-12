package faultlib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeSSHOnPath writes a fake `ssh` executable that appends its own
// argv to capturePath (one line, space-joined) and exits 0, then prepends
// its directory to PATH for the duration of the test — so execSSH's real
// os/exec.CommandContext("ssh", ...) call can be inspected without actually
// connecting anywhere.
func writeFakeSSHOnPath(t *testing.T) (capturePath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh script is POSIX shell only")
	}
	dir := t.TempDir()
	capturePath = filepath.Join(dir, "captured_args")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\ncat >/dev/null\nexit 0\n", capturePath)
	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })            //nolint:errcheck
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath) //nolint:errcheck
	return capturePath
}

// TestExecSSH_PassesPortFlag is a regression test for a real gap found
// 2026-10-08: execSSH's own `ssh` invocation had no port flag at all
// (always connects to port 22), while agents/sysadmin/sshexec.go's sshRun
// (the agent's own SSH dispatch) already correctly honors a per-target
// port — needed since a real VM's sshd may not be reachable on port 22
// from wherever faulttest runs. Confirms `-p <port>` is passed when
// cfg.SSHPort is set, and omitted (falling back to ssh's own default) when
// it's zero, so every existing ssh_exec fault (none of which set it) keeps
// behaving identically.
func TestExecSSH_PassesPortFlag(t *testing.T) {
	t.Run("port set", func(t *testing.T) {
		capturePath := writeFakeSSHOnPath(t)
		inj := NewInjector(&HarnessConfig{SSHPort: 2222})
		if err := inj.execSSH(context.Background(), InjectSpec{ExecVia: "testuser@example.com", ScriptInline: "echo hi"}); err != nil {
			t.Fatalf("execSSH: %v", err)
		}
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !strings.Contains(string(data), "-p 2222") {
			t.Errorf("captured ssh args = %q, want it to contain \"-p 2222\"", data)
		}
	})

	t.Run("port unset falls back to ssh's own default", func(t *testing.T) {
		capturePath := writeFakeSSHOnPath(t)
		inj := NewInjector(&HarnessConfig{})
		if err := inj.execSSH(context.Background(), InjectSpec{ExecVia: "testuser@example.com", ScriptInline: "echo hi"}); err != nil {
			t.Fatalf("execSSH: %v", err)
		}
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if strings.Contains(string(data), "-p ") {
			t.Errorf("captured ssh args = %q, want no -p flag when SSHPort is unset", data)
		}
	})
}
