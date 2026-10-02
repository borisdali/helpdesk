package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"simple", "'simple'"},
		{"", "''"},
		{"has space", "'has space'"},
		{"it's got a quote", `'it'\''s got a quote'`},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"`backtick`", "'`backtick`'"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	got := shellCommand("pgbackrest", []string{"info", "--output=json", "--stanza=main db"}, []string{"PGPASSWORD=p'q"})
	want := `PGPASSWORD='p'\''q' 'pgbackrest' 'info' '--output=json' '--stanza=main db'`
	if got != want {
		t.Errorf("shellCommand() = %q, want %q", got, want)
	}
}

// generateTestRSAKey creates a real 2048-bit RSA key, writes it to a temp
// file as a classic PKCS1 PEM block (the format ssh.ParsePrivateKey — and
// therefore loadSSHAuth — natively parses), and returns both the file path
// and the corresponding ssh.PublicKey for use in test-server authorization.
func generateTestRSAKey(t *testing.T, dir, name string) (path string, pub ssh.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	return path, signer.PublicKey()
}

// testSSHServer is a minimal, real in-process SSH server: real TCP, real
// handshake, real public-key auth, real "session"/"exec" channel handling —
// used to prove sshRun's dial→auth→exec→capture-output→exit-status path
// actually works end to end, not just that it compiles against the ssh
// package's types.
type testSSHServer struct {
	addr    string
	hostPub ssh.PublicKey
}

func startTestSSHServer(t *testing.T, authorizedPub ssh.PublicKey, handler func(cmd string) (output string, exitCode uint32)) *testSSHServer {
	t.Helper()
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey (host key): %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey (host key): %v", err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if authorizedPub != nil && bytes.Equal(key.Marshal(), authorizedPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized public key")
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() }) //nolint:errcheck

	go func() {
		for {
			nConn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveTestSSHConn(nConn, config, handler)
		}
	}()

	return &testSSHServer{addr: listener.Addr().String(), hostPub: hostSigner.PublicKey()}
}

func serveTestSSHConn(nConn net.Conn, config *ssh.ServerConfig, handler func(cmd string) (string, uint32)) {
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return
	}
	defer sconn.Close() //nolint:errcheck
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type") //nolint:errcheck
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close() //nolint:errcheck
			for req := range requests {
				if req.Type != "exec" {
					if req.WantReply {
						req.Reply(false, nil) //nolint:errcheck
					}
					continue
				}
				var msg struct{ Command string }
				ssh.Unmarshal(req.Payload, &msg) //nolint:errcheck
				if req.WantReply {
					req.Reply(true, nil) //nolint:errcheck
				}
				output, exitCode := handler(msg.Command)
				channel.Write([]byte(output)) //nolint:errcheck
				statusMsg := struct{ Status uint32 }{exitCode}
				channel.SendRequest("exit-status", false, ssh.Marshal(&statusMsg)) //nolint:errcheck
				return
			}
		}()
	}
}

// writeKnownHostsFile writes a known_hosts entry for addr/pub using the same
// knownhosts.Line helper sshHostKeyCallback's real caller would rely an
// operator having produced via `ssh-keyscan`, and points SSH_KNOWN_HOSTS at
// it for the duration of the test.
func writeKnownHostsFile(t *testing.T, addr string, pub ssh.PublicKey) string {
	t.Helper()
	line := knownhosts.Line([]string{addr}, pub)
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func hostPortOf(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parsing port %q: %v", portStr, err)
	}
	return host, port
}

func TestRunOnHost_SSHRoundTrip_Success(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		if cmd != `'echo' 'hello world'` {
			return "unexpected command: " + cmd, 1
		}
		return "hello world\n", 0
	})

	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	host := resolvedHost{
		VMAddress:  addr,
		SSHUser:    "testuser",
		SSHPort:    port,
		SSHKeyPath: keyPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := runOnHost(ctx, host, "echo", []string{"hello world"}, nil)
	if err != nil {
		t.Fatalf("runOnHost() error = %v", err)
	}
	if output != "hello world\n" {
		t.Errorf("runOnHost() output = %q, want %q", output, "hello world\n")
	}
}

func TestRunOnHost_SSHRoundTrip_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) {
		return "boom\n", 1
	})

	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                   //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, srv.hostPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	host := resolvedHost{VMAddress: addr, SSHUser: "testuser", SSHPort: port, SSHKeyPath: keyPath}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := runOnHost(ctx, host, "false", nil, nil)
	if err == nil {
		t.Fatal("runOnHost() error = nil, want a non-zero-exit error")
	}
	if output != "boom\n" {
		t.Errorf("runOnHost() output = %q, want %q (output should still be captured on failure)", output, "boom\n")
	}
}

// TestRunOnHost_WrongHostKey_Rejected proves host-key verification is
// actually enforced, not just wired: a known_hosts file pinned to a
// DIFFERENT key than the server presents must fail the connection.
func TestRunOnHost_WrongHostKey_Rejected(t *testing.T) {
	dir := t.TempDir()
	keyPath, clientPub := generateTestRSAKey(t, dir, "id_rsa")

	srv := startTestSSHServer(t, clientPub, func(cmd string) (string, uint32) { return "", 0 })

	// Known-hosts entry for the right address, but a DIFFERENT (unrelated)
	// public key than the one the server will actually present.
	_, wrongPub := generateTestRSAKey(t, dir, "unrelated")
	oldKH := os.Getenv("SSH_KNOWN_HOSTS")
	t.Cleanup(func() { os.Setenv("SSH_KNOWN_HOSTS", oldKH) })                //nolint:errcheck
	os.Setenv("SSH_KNOWN_HOSTS", writeKnownHostsFile(t, srv.addr, wrongPub)) //nolint:errcheck

	addr, port := hostPortOf(t, srv.addr)
	host := resolvedHost{VMAddress: addr, SSHUser: "testuser", SSHPort: port, SSHKeyPath: keyPath}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := runOnHost(ctx, host, "echo", []string{"should not run"}, nil); err == nil {
		t.Fatal("runOnHost() error = nil, want a host-key mismatch error")
	}
}

func TestRunOnHost_LocalFallback_WhenSSHFieldsUnset(t *testing.T) {
	defer withMockRunner("local output", nil)()
	host := resolvedHost{} // no SSHUser/SSHKeyPath set
	output, err := runOnHost(context.Background(), host, "echo", []string{"hi"}, nil)
	if err != nil {
		t.Fatalf("runOnHost() error = %v", err)
	}
	if output != "local output" {
		t.Errorf("runOnHost() output = %q, want %q (should use cmdRunner, not SSH)", output, "local output")
	}
}

func TestLoadSSHAuth_PlainKey(t *testing.T) {
	dir := t.TempDir()
	keyPath, wantPub := generateTestRSAKey(t, dir, "id_rsa")

	signer, err := loadSSHAuth(keyPath)
	if err != nil {
		t.Fatalf("loadSSHAuth() error = %v", err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), wantPub.Marshal()) {
		t.Error("loadSSHAuth() returned a signer whose public key doesn't match the key file")
	}
}

// TestLoadSSHAuth_CertificateSigner proves the short-lived-credential design
// claim directly: a key file with a sibling "-cert.pub" signed certificate
// is loaded and wrapped correctly, exactly the shape a Vault SSH secrets
// engine (or any CA-based short-lived-credential issuer) produces.
func TestLoadSSHAuth_CertificateSigner(t *testing.T) {
	dir := t.TempDir()
	keyPath, keyPub := generateTestRSAKey(t, dir, "id_rsa")

	caKeyPath, _ := generateTestRSAKey(t, dir, "ca")
	caKeyBytes, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatalf("ReadFile(ca key): %v", err)
	}
	caSigner, err := ssh.ParsePrivateKey(caKeyBytes)
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey(ca): %v", err)
	}

	cert := &ssh.Certificate{
		Key:             keyPub,
		Serial:          1,
		CertType:        ssh.UserCert,
		KeyId:           "test-short-lived-cred",
		ValidPrincipals: []string{"testuser"},
		ValidAfter:      0,
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("cert.SignCert: %v", err)
	}
	certPath := keyPath + "-cert.pub"
	if err := os.WriteFile(certPath, ssh.MarshalAuthorizedKey(cert), 0600); err != nil {
		t.Fatalf("WriteFile(cert): %v", err)
	}

	signer, err := loadSSHAuth(keyPath)
	if err != nil {
		t.Fatalf("loadSSHAuth() error = %v", err)
	}
	gotCert, ok := signer.PublicKey().(*ssh.Certificate)
	if !ok {
		t.Fatalf("loadSSHAuth() returned a plain-key signer, want a certificate signer (PublicKey type = %T)", signer.PublicKey())
	}
	if gotCert.KeyId != "test-short-lived-cred" {
		t.Errorf("certificate KeyId = %q, want test-short-lived-cred (loadSSHAuth should have loaded the sibling cert file, not just the key)", gotCert.KeyId)
	}
}
