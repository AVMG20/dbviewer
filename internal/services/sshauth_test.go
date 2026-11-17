package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dbviewer/internal/model"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// OpenSSH-like auth chain tests: temp HOME, generated keys, in-process agent and
// SSH servers. Nothing touches the real ~/.ssh or agent.

type testKey struct {
	priv   ed25519.PrivateKey
	signer ssh.Signer
}

func genKey(t *testing.T) testKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ssh.NewSignerFromKey(priv)
	return testKey{priv: priv, signer: s}
}

// writeKey writes an OpenSSH private key (encrypted when passphrase != "") and its .pub.
func writeKey(t *testing.T, path string, k testKey, passphrase string) {
	var block *pem.Block
	var err error
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(k.priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(k.priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(k.signer.PublicKey()), 0o644)
}

// startAgent serves an in-process agent with the given keys and points SSH_AUTH_SOCK at it.
func startAgent(t *testing.T, keys ...testKey) {
	dir, err := os.MkdirTemp("/tmp", "dbva")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	kr := agent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(agent.AddedKey{PrivateKey: k.priv}); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { agent.ServeAgent(kr, c); c.Close() }()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

// trust writes the server's host key into ~/.ssh/known_hosts of the temp HOME.
func trust(t *testing.T, addr string, hostKey ssh.Signer) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".ssh", "known_hosts")
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(addr)}, hostKey.PublicKey()) + "\n")
	f.Close()
}

func sshCfg(f *fixture, host string, port int, user string) model.ConnectionConfig {
	pgpw := "dbviewer"
	return model.ConnectionConfig{ProjectID: f.pid, Name: "pg via ssh", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432,
		User: "postgres", Password: &pgpw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable,
		SSH: model.SSHTunnel{Enabled: true, Host: host, Port: port, User: user, AuthMethod: "agent"}}
}

func homeSSH(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", name)
}

func triedString(r []model.SSHAuthAttempt) string {
	var parts []string
	for _, a := range r {
		parts = append(parts, a.Method+"|"+a.Key+"|"+a.Result)
	}
	return strings.Join(parts, "; ")
}

func TestSSHAutoAgentEmptyDefaultKey(t *testing.T) {
	f := newFixture(t)
	startAgent(t) // agent without keys, like the macOS GUI agent
	client := genKey(t)
	writeKey(t, homeSSH("id_ed25519"), client, "")
	hk := newSigner(t)
	srv := startSSHServer(t, hk, "secret", client.signer.PublicKey())
	trust(t, srv.addr, hk)
	host, ps, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(ps)

	r, err := f.cs.TestSSH(f.ctx, sshCfg(f, host, port, "tunnel"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.AuthMethod != "key: ~/.ssh/id_ed25519" || !r.DBReachable || r.ServerVersion == "" || !strings.HasPrefix(r.HostKeyFingerprint, "ssh-ed25519 SHA256:") {
		t.Fatalf("result: %+v", r)
	}
	if !strings.Contains(triedString(r.Tried), "agent||no keys") || !strings.Contains(triedString(r.Tried), "key|~/.ssh/id_ed25519|accepted") {
		t.Fatalf("tried: %s", triedString(r.Tried))
	}
	// the real connection uses the same chain
	if _, err := f.cs.TestConnection(f.ctx, sshCfg(f, host, port, "tunnel")); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	// unreachable database endpoint: login fine, dbReachable false
	bad := sshCfg(f, host, port, "tunnel")
	bad.Port = 1
	if r, err := f.cs.TestSSH(f.ctx, bad); err != nil || r.DBReachable || r.DBError == "" {
		t.Fatalf("unreachable db: %+v %v", r, err)
	}
}

func TestSSHAutoAllRejectedMessage(t *testing.T) {
	f := newFixture(t)
	startAgent(t)
	writeKey(t, homeSSH("id_ed25519"), genKey(t), "")
	writeKey(t, homeSSH("id_rsa"), genKey(t), "secretpass") // encrypted, not authorized
	hk := newSigner(t)
	srv := startSSHServer(t, hk, "secret", genKey(t).signer.PublicKey())
	trust(t, srv.addr, hk)
	host, ps, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(ps)
	_, err := f.cs.TestSSH(f.ctx, sshCfg(f, host, port, "tunnel"))
	if err == nil || !strings.HasPrefix(err.Error(), model.ErrSSH) {
		t.Fatalf("expected ssh_error, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"agent (0 keys)", "~/.ssh/id_ed25519 (rejected)", "~/.ssh/id_rsa (encrypted, no passphrase)", "ssh-add --apple-use-keychain ~/.ssh/id_ed25519"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "PRIVATE KEY") || strings.Contains(msg, "secretpass") {
		t.Fatalf("key material in message: %s", msg)
	}
}

func TestSSHConfigIdentityFileAndAlias(t *testing.T) {
	f := newFixture(t)
	startAgent(t)
	client := genKey(t)
	home, _ := os.UserHomeDir()
	writeKey(t, filepath.Join(home, "keys", "special_key"), client, "")
	writeKey(t, homeSSH("id_ed25519"), genKey(t), "") // not authorized; IdentitiesOnly skips it
	hk := newSigner(t)
	srv := startSSHServer(t, hk, "secret", client.signer.PublicKey())
	trust(t, srv.addr, hk)
	host, ps, _ := net.SplitHostPort(srv.addr)
	conf := "Host *.example\n  User nobody\n\nHost mydb\n  HostName " + host + "\n  Port " + ps + "\n  User tunnel\n  IdentityFile ~/keys/special_key\n  IdentitiesOnly yes\n\nHost *\n  User fallback\n"
	os.WriteFile(homeSSH("config"), []byte(conf), 0o600)

	// alias only: HostName, Port, User and IdentityFile come from the config
	r, err := f.cs.TestSSH(f.ctx, sshCfg(f, "mydb", 0, ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Address != srv.addr || r.User != "tunnel" || r.AuthMethod != "key: ~/keys/special_key" {
		t.Fatalf("result: %+v", r)
	}
	if strings.Contains(triedString(r.Tried), "id_ed25519") {
		t.Fatalf("IdentitiesOnly ignored: %s", triedString(r.Tried))
	}
	// form fields win over the config
	if r, err := f.cs.TestSSH(f.ctx, sshCfg(f, "mydb", 0, "someone")); err != nil || r.User != "someone" {
		t.Fatalf("form user did not override config user: %+v %v", r, err)
	}
	os.WriteFile(homeSSH("config"), []byte(strings.Replace(conf, "Port "+ps, "Port 1", 1)), 0o600)
	p, _ := strconv.Atoi(ps)
	if r, err := f.cs.TestSSH(f.ctx, sshCfg(f, "mydb", p, "")); err != nil || r.Address != srv.addr {
		t.Fatalf("form port did not override config port: %+v %v", r, err)
	}
}

func TestSSHEncryptedKeyPassphraseFlow(t *testing.T) {
	f := newFixture(t)
	startAgent(t)
	client := genKey(t)
	keyPath := homeSSH("id_ed25519")
	writeKey(t, keyPath, client, "correct horse")
	hk := newSigner(t)
	srv := startSSHServer(t, hk, "secret", client.signer.PublicKey())
	trust(t, srv.addr, hk)
	host, ps, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(ps)
	var prompts []model.SSHKeyPrompt
	f.core.Conns.OnKeyPassphrase = func(p model.SSHKeyPrompt) { prompts = append(prompts, p) }

	_, err := f.cs.TestSSH(f.ctx, sshCfg(f, host, port, "tunnel"))
	if err == nil || !strings.HasPrefix(err.Error(), model.ErrPassphraseRequired) || len(prompts) != 1 || prompts[0].KeyPath != keyPath {
		t.Fatalf("expected passphrase_required + prompt, got %v %+v", err, prompts)
	}
	if err := f.cs.ProvideKeyPassphrase(keyPath, "wrong", false); err == nil || !strings.HasPrefix(err.Error(), model.ErrInvalid) {
		t.Fatalf("wrong passphrase accepted: %v", err)
	}
	if err := f.cs.ProvideKeyPassphrase(keyPath, "correct horse", false); err != nil {
		t.Fatal(err)
	}
	r, err := f.cs.TestSSH(f.ctx, sshCfg(f, host, port, "tunnel"))
	if err != nil || r.AuthMethod != "key: ~/.ssh/id_ed25519" {
		t.Fatalf("after passphrase: %+v %v", r, err)
	}
	// not remembered: a new app session asks again
	core2, err := NewCore() // same data dir: nothing was stored in the secrets
	if err != nil {
		t.Fatal(err)
	}
	defer core2.Close()
	if _, err := NewConnectionService(core2).TestSSH(f.ctx, sshCfg(f, host, port, "tunnel")); err == nil || !strings.HasPrefix(err.Error(), model.ErrPassphraseRequired) {
		t.Fatalf("passphrase persisted without opt-in: %v", err)
	}
	// opt-in: remembered in the secrets file
	if err := f.cs.ProvideKeyPassphrase(keyPath, "correct horse", true); err != nil {
		t.Fatal(err)
	}
	core3, _ := NewCore()
	defer core3.Close()
	if _, err := NewConnectionService(core3).TestSSH(f.ctx, sshCfg(f, host, port, "tunnel")); err != nil {
		t.Fatalf("remembered passphrase not used: %v", err)
	}
}

func TestSSHAgentHoldsEncryptedKey(t *testing.T) {
	f := newFixture(t)
	client := genKey(t)
	startAgent(t, client) // the agent already has the key of the encrypted file
	writeKey(t, homeSSH("id_ed25519"), client, "secret phrase")
	hk := newSigner(t)
	srv := startSSHServer(t, hk, "secret", client.signer.PublicKey())
	trust(t, srv.addr, hk)
	host, ps, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(ps)
	var prompts int
	f.core.Conns.OnKeyPassphrase = func(model.SSHKeyPrompt) { prompts++ }
	r, err := f.cs.TestSSH(f.ctx, sshCfg(f, host, port, "tunnel"))
	if err != nil || !strings.HasPrefix(r.AuthMethod, "agent: ED25519 SHA256:") || prompts != 0 {
		t.Fatalf("agent should be used without a passphrase prompt: %+v %v (%d prompts)", r, err, prompts)
	}
}

func TestSSHProxyJumpAlias(t *testing.T) {
	f := newFixture(t)
	client := genKey(t)
	startAgent(t, client)
	jhk, thk := newSigner(t), newSigner(t)
	jump := startSSHServer(t, jhk, "x", client.signer.PublicKey())
	target := startSSHServer(t, thk, "x", client.signer.PublicKey())
	trust(t, jump.addr, jhk)
	trust(t, target.addr, thk)
	jh, jp, _ := net.SplitHostPort(jump.addr)
	th, tp, _ := net.SplitHostPort(target.addr)
	conf := "Host bastion\n  HostName " + jh + "\n  Port " + jp + "\n  User tunnel\n\nHost inner\n  HostName " + th + "\n  Port " + tp + "\n  User tunnel\n  ProxyJump bastion\n"
	os.WriteFile(homeSSH("config"), []byte(conf), 0o600)
	r, err := f.cs.TestSSH(f.ctx, sshCfg(f, "inner", 0, ""))
	if err != nil || r.ProxyJump != "bastion" || !r.DBReachable || r.Address != target.addr {
		t.Fatalf("proxyjump: %+v %v", r, err)
	}
	os.WriteFile(homeSSH("config"), []byte(strings.Replace(conf, "ProxyJump bastion", "ProxyJump bastion,bastion", 1)), 0o600)
	if _, err := f.cs.TestSSH(f.ctx, sshCfg(f, "inner", 0, "")); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("multi-hop: %v", err)
	}
}
