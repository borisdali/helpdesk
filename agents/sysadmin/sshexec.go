package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// runOnHost is the dispatch point every SSH-capable sysadmin tool should call
// instead of cmdRunner.Run directly: if host was resolved from a VM entry
// with SSHUser and SSHKeyPath both set, the command runs over SSH; otherwise
// it falls back to today's local exec.CommandContext path unchanged. This is
// the only place that decision gets made, so callers never need their own
// local-vs-remote branch.
//
// env is applied by prefixing the remote command line (e.g. "FOO=bar cmd
// arg1 arg2"), not via the SSH protocol's own session.Setenv — many OpenSSH
// servers reject client-set environment variables unless AcceptEnv/
// PermitUserEnvironment is explicitly configured, which this project has no
// way to assume or require on a customer's host.
func runOnHost(ctx context.Context, host resolvedHost, name string, args []string, env []string) (string, error) {
	if host.SSHUser == "" || host.SSHKeyPath == "" {
		return cmdRunner.Run(ctx, name, args, env)
	}
	return sshRun(ctx, host, name, args, env)
}

// sshRun opens one SSH connection, runs one command, and closes the
// connection. No pooling/reuse across calls — sysadmin tools are invoked
// infrequently enough (diagnostic checks, not a hot path) that per-call
// connection setup cost is not worth the complexity of a connection cache.
func sshRun(ctx context.Context, host resolvedHost, name string, args []string, env []string) (string, error) {
	signer, err := loadSSHAuth(host.SSHKeyPath)
	if err != nil {
		return "", fmt.Errorf("ssh: loading key %s: %w", host.SSHKeyPath, err)
	}
	hostKeyCallback, err := sshHostKeyCallback()
	if err != nil {
		return "", fmt.Errorf("ssh: host key verification setup: %w", err)
	}

	port := host.SSHPort
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host.VMAddress, fmt.Sprintf("%d", port))

	config := &ssh.ClientConfig{
		User:            host.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	dialer := &net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("ssh: dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("ssh: handshake with %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh: new session on %s: %w", addr, err)
	}
	defer session.Close()

	command := shellCommand(name, args, env)

	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- result{output: string(output), err: runErr}
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL) //nolint:errcheck
		return "", ctx.Err()
	case r := <-done:
		return r.output, r.err
	}
}

// shellCommand builds a single POSIX shell command line from a program name,
// its arguments, and optional VAR=value environment prefixes — every part
// individually quoted so a value containing spaces, quotes, or shell
// metacharacters can never be interpreted as a separate word or command.
func shellCommand(name string, args []string, env []string) string {
	parts := make([]string, 0, len(env)+1+len(args))
	for _, e := range env {
		// env entries are already "VAR=value" — quote only the value half so
		// the assignment syntax itself survives.
		if i := strings.IndexByte(e, '='); i >= 0 {
			parts = append(parts, e[:i+1]+shellQuote(e[i+1:]))
		} else {
			parts = append(parts, shellQuote(e))
		}
	}
	parts = append(parts, shellQuote(name))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps s in single quotes, POSIX-escaping any single quote it
// contains (' -> '\”), so the result is always safe to place unquoted in a
// shell command line regardless of what s contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// loadSSHAuth reads the private key at keyPath fresh (never cached) and
// returns an ssh.AuthMethod for it. When a sibling "<keyPath>-cert.pub" file
// exists, it's loaded as an SSH certificate and wrapped around the key
// signer — this is what makes short-lived credentials (a Vault SSH secrets
// engine lease, a cloud provider's ephemeral cert) work with zero special
// handling here: whatever refreshes that file on disk (a Vault Agent
// sidecar, a login helper) is invisible to this code, which just reads
// whatever is there right now.
func loadSSHAuth(keyPath string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	certPath := keyPath + "-cert.pub"
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return signer, nil
		}
		return nil, fmt.Errorf("reading certificate %s: %w", certPath, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate %s: %w", certPath, err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%s does not contain an SSH certificate", certPath)
	}
	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, fmt.Errorf("building certificate signer from %s: %w", certPath, err)
	}
	return certSigner, nil
}

// sshHostKeyCallback builds a host-key verification callback from the
// operator's own known_hosts file (~/.ssh/known_hosts, or $SSH_KNOWN_HOSTS
// when set — no HELPDESK_-specific override; this deliberately reuses
// whatever the operator's own ssh client already trusts). Falls back to
// InsecureIgnoreHostKey only when no known_hosts file exists at all, with a
// loud warning — matching this project's standing "fail loud, not silently
// weaker" convention (e.g. HELPDESK_MODEL_NAME's own trust-gate warning) —
// rather than silently accepting any host key.
func sshHostKeyCallback() (ssh.HostKeyCallback, error) {
	path := os.Getenv("SSH_KNOWN_HOSTS")
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".ssh", "known_hosts")
		}
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return knownhosts.New(path)
		}
	}
	slog.Warn("ssh: no known_hosts file found — host key verification disabled for this connection",
		"checked_path", path,
		"fix", "create a known_hosts file (ssh-keyscan <host> >> ~/.ssh/known_hosts) or set SSH_KNOWN_HOSTS")
	return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit, logged fallback — see comment above
}
