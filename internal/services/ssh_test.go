package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dbviewer/internal/model"

	"golang.org/x/crypto/ssh"
)

// testSSHServer is a minimal in-process SSH server that supports password and
// public key auth and direct-tcpip forwarding (what the tunnel uses).
type testSSHServer struct {
	addr string
	ln   net.Listener
	mu   sync.Mutex
	conf *ssh.ServerConfig
}

func newSigner(t *testing.T) ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startSSHServer(t *testing.T, hostKey ssh.Signer, password string, clientKey ssh.PublicKey, extraHostKeys ...ssh.Signer) *testSSHServer {
	conf := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "tunnel" && string(pw) == password {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && string(k.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	conf.AddHostKey(hostKey)
	for _, k := range extraHostKeys {
		conf.AddHostKey(k)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{addr: ln.Addr().String(), ln: ln, conf: conf}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}

func (s *testSSHServer) handle(c net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(c, s.conf)
	if err != nil {
		c.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		target, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() {
			defer ch.Close()
			defer target.Close()
			go io.Copy(target, ch)
			io.Copy(ch, target)
		}()
	}
}

func TestSSHTunnel(t *testing.T) {
	f := newFixture(t)
	hostKey := newSigner(t)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	srv := startSSHServer(t, hostKey, "secret", clientSigner.PublicKey())
	host, portS, _ := net.SplitHostPort(srv.addr)
	port, _ := strconv.Atoi(portS)

	var prompts []model.HostKeyPrompt
	f.core.Conns.OnHostKey = func(p model.HostKeyPrompt) { prompts = append(prompts, p) }

	pgpw, sshpw := "dbviewer", "secret"
	cfg := model.ConnectionConfig{ProjectID: f.pid, Name: "pg via ssh", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432,
		User: "postgres", Password: &pgpw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable,
		SSH: model.SSHTunnel{Enabled: true, Host: host, Port: port, User: "tunnel", AuthMethod: "password", Password: &sshpw}}

	// 1. unknown host key: refused with a prompt
	_, err := f.cs.TestConnection(f.ctx, cfg)
	if err == nil || !strings.HasPrefix(err.Error(), model.ErrHostKeyUnknown) || len(prompts) != 1 {
		t.Fatalf("expected host_key_unknown + prompt, got %v, %d prompts", err, len(prompts))
	}
	if prompts[0].Fingerprint != ssh.FingerprintSHA256(hostKey.PublicKey()) {
		t.Fatalf("fingerprint mismatch: %+v", prompts[0])
	}
	// 2. trust it, then connect works (password auth)
	if err := f.cs.TrustHostKey(prompts[0].Host, prompts[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	if r, err := f.cs.TestConnection(f.ctx, cfg); err != nil || !strings.Contains(r.ServerVersion, "PostgreSQL") {
		t.Fatalf("tunnel test: %+v %v", r, err)
	}
	saved := f.save(cfg)
	if _, err := f.cs.Connect(f.ctx, saved.ID, ""); err != nil {
		t.Fatalf("connect via tunnel: %v", err)
	}
	if page, err := f.ds.FetchPage(f.ctx, model.PageRequest{ConnID: saved.ID, Table: "store"}); err != nil || len(page.Rows) != 4 {
		t.Fatalf("query via tunnel: %v", err)
	}
	if err := f.cs.Disconnect(saved.ID); err != nil {
		t.Fatal(err)
	}

	// 3. key auth with an encrypted key
	pass := "keypass"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(clientPriv, "", []byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	kc := cfg
	kc.SSH = model.SSHTunnel{Enabled: true, Host: host, Port: port, User: "tunnel", AuthMethod: "key", KeyPath: keyPath}
	if _, err := f.cs.TestConnection(f.ctx, kc); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("expected passphrase error, got %v", err)
	}
	kc.SSH.KeyPassphrase = &pass
	if _, err := f.cs.TestConnection(f.ctx, kc); err != nil {
		t.Fatalf("key auth: %v", err)
	}
	// wrong password
	bad := "nope"
	wc := cfg
	wc.SSH.Password = &bad
	if _, err := f.cs.TestConnection(f.ctx, wc); err == nil || !strings.HasPrefix(err.Error(), model.ErrSSH) {
		t.Fatalf("expected ssh_error for wrong password, got %v", err)
	}

	// 4. changed host key on the same address: hard failure
	srv.ln.Close()
	srv2 := startSSHServerOn(t, srv.addr, newSigner(t), "secret")
	_ = srv2
	if _, err := f.cs.TestConnection(f.ctx, cfg); err == nil || !strings.HasPrefix(err.Error(), model.ErrHostKeyMismatch) {
		t.Fatalf("expected host_key_mismatch, got %v", err)
	}
}

func startSSHServerOn(t *testing.T, addr string, hostKey ssh.Signer, password string) *testSSHServer {
	conf := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) == password {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	conf.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot rebind %s: %v", addr, err)
	}
	s := &testSSHServer{addr: addr, ln: ln, conf: conf}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}
