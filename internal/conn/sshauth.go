package conn

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbviewer/internal/model"
	"dbviewer/internal/store"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// LaunchctlAgentSocket returns the user's agent socket from launchd when the
// GUI app has no SSH_AUTH_SOCK. A variable so tests can replace it.
var LaunchctlAgentSocket = func() string {
	out, err := exec.Command("launchctl", "getenv", "SSH_AUTH_SOCK").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// defaultIdentityFiles are tried (like OpenSSH) when IdentitiesOnly is not set.
var defaultIdentityFiles = []string{"id_ed25519", "id_ecdsa", "id_ecdsa_sk", "id_ed25519_sk", "id_rsa"}

// keyPassphrases caches passphrases entered in this session (path -> passphrase).
type keyPassphrases struct {
	mu sync.Mutex
	m  map[string]string
}

// sshTarget is a resolved SSH endpoint (form fields over ~/.ssh/config).
type sshTarget struct {
	alias     string
	host      string
	port      int
	user      string
	addr      string
	conf      sshHostConfig
	proxyJump string
}

// resolveSSHTarget applies ~/.ssh/config to the form fields; form values win.
func resolveSSHTarget(t model.SSHTunnel) sshTarget {
	hc := resolveSSHConfig(t.Host)
	r := sshTarget{alias: t.Host, host: t.Host, conf: hc, proxyJump: hc.ProxyJump}
	if hc.HostName != "" {
		r.host = expandSSHTokens(hc.HostName, t.Host, "")
	}
	r.port = t.Port
	if r.port == 0 {
		r.port = hc.Port
	}
	if r.port == 0 {
		r.port = 22
	}
	r.user = t.User
	if r.user == "" {
		r.user = hc.User
	}
	if r.user == "" {
		r.user = osUser()
	}
	r.addr = net.JoinHostPort(r.host, strconv.Itoa(r.port))
	return r
}

// authState collects what was tried during one login.
type authState struct {
	mu        sync.Mutex
	tried     []model.SSHAuthAttempt
	signers   []*trackedSigner
	used      string // description of the method that succeeded
	needPass  *model.SSHKeyPrompt
	agentKeys int
	agentOK   bool
}

func (a *authState) add(method, key, result string) {
	a.mu.Lock()
	a.tried = append(a.tried, model.SSHAuthAttempt{Method: method, Key: key, Result: result})
	a.mu.Unlock()
}

var errNeedPassphrase = errors.New("passphrase required")

// trackedSigner wraps a signer to learn which key the server accepted while
// keeping the RSA SHA-2 algorithm negotiation of the wrapped signer.
type trackedSigner struct {
	inner   ssh.Signer
	pub     ssh.PublicKey
	method  string // agent | key
	label   string
	lazy    *lazyKey // encrypted key file without passphrase
	state   *authState
	attempt int // index into state.tried
	signed  bool
}

type lazyKey struct{ path string }

func (s *trackedSigner) PublicKey() ssh.PublicKey { return s.pub }

func (s *trackedSigner) Algorithms() []string {
	if m, ok := s.inner.(ssh.MultiAlgorithmSigner); ok {
		return m.Algorithms()
	}
	base := s.pub.Type()
	if c, ok := s.pub.(*ssh.Certificate); ok {
		base = c.Key.Type() // the list must not contain certificate types
	}
	if base == ssh.KeyAlgoRSA {
		if _, ok := s.inner.(ssh.AlgorithmSigner); ok || s.lazy != nil {
			return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
	}
	return []string{base}
}

func (s *trackedSigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	return s.SignWithAlgorithm(r, data, "")
}

func (s *trackedSigner) SignWithAlgorithm(r io.Reader, data []byte, algo string) (*ssh.Signature, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.lazy != nil {
		// the server accepted this public key: its passphrase is needed now
		s.state.tried[s.attempt].Result = "passphrase required"
		s.state.needPass = &model.SSHKeyPrompt{KeyPath: s.lazy.path, KeyType: s.pub.Type(), Fingerprint: ssh.FingerprintSHA256(s.pub)}
		return nil, errNeedPassphrase
	}
	var sig *ssh.Signature
	var err error
	if as, ok := s.inner.(ssh.AlgorithmSigner); ok && algo != "" {
		sig, err = as.SignWithAlgorithm(r, data, algo)
	} else {
		sig, err = s.inner.Sign(r, data)
	}
	if err != nil {
		s.state.tried[s.attempt].Result = "error: " + err.Error()
		return nil, err
	}
	s.signed = true
	s.state.tried[s.attempt].Result = "accepted"
	s.state.used = s.method + ": " + s.label
	return sig, nil
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

func keyLabel(pub ssh.PublicKey) string {
	t := strings.TrimPrefix(strings.ToUpper(strings.TrimPrefix(pub.Type(), "ssh-")), "ECDSA-SHA2-")
	switch pub.Type() {
	case ssh.KeyAlgoED25519:
		t = "ED25519"
	case ssh.KeyAlgoRSA:
		t = "RSA"
	}
	if strings.HasPrefix(pub.Type(), "ecdsa-") {
		t = "ECDSA"
	}
	return t + " " + ssh.FingerprintSHA256(pub)
}

// agentSocket picks the agent: IdentityAgent from ~/.ssh/config, else
// SSH_AUTH_SOCK, else launchd's SSH_AUTH_SOCK (GUI apps often lack the env var).
func agentSocket(tg sshTarget) (string, string) {
	if ia := tg.conf.IdentityAgent; ia != "" {
		switch {
		case strings.EqualFold(ia, "none"):
			return "", "disabled by IdentityAgent none"
		case ia == "SSH_AUTH_SOCK":
		default:
			return expandSSHTokens(strings.Trim(ia, `"`), tg.host, tg.user), ""
		}
	}
	if s := os.Getenv("SSH_AUTH_SOCK"); s != "" {
		return s, ""
	}
	if s := LaunchctlAgentSocket(); s != "" {
		return s, ""
	}
	return "", "no agent (SSH_AUTH_SOCK not set)"
}

// passphraseFor returns a known passphrase for a key file ("" if none).
func (m *Manager) passphraseFor(path string, explicit *string) string {
	if explicit != nil && *explicit != "" {
		return *explicit
	}
	m.keyPass.mu.Lock()
	p := m.keyPass.m[path]
	m.keyPass.mu.Unlock()
	if p != "" {
		return p
	}
	if v, err := m.store.SSHKeyPassphrase(path); err == nil {
		return v
	}
	return ""
}

// ProvideKeyPassphrase checks a passphrase against the key file and keeps it for
// this session (remember=true also saves it in the secrets file).
func (m *Manager) ProvideKeyPassphrase(path, passphrase string, remember bool) error {
	path = expandHome(path)
	pem, err := os.ReadFile(path)
	if err != nil {
		return model.Errf(model.ErrInvalid, "reading SSH key %s: %v", tildePath(path), err)
	}
	if _, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase)); err != nil {
		if errors.Is(err, x509.IncorrectPasswordError) || strings.Contains(err.Error(), "decrypt") || strings.Contains(err.Error(), "passphrase") {
			return model.Errf(model.ErrInvalid, "wrong passphrase for %s", tildePath(path))
		}
		return model.Errf(model.ErrInvalid, "SSH key %s: %v", tildePath(path), err)
	}
	m.keyPass.mu.Lock()
	m.keyPass.m[path] = passphrase
	m.keyPass.mu.Unlock()
	if remember {
		if err := m.store.SetSSHKeyPassphrase(path, passphrase); err != nil {
			return model.Wrap(model.ErrInternal, err)
		}
	}
	return nil
}

// loadKeyFile turns a key file into a tracked signer (lazy when encrypted and
// no passphrase is known). ok=false when the file was skipped.
func (m *Manager) loadKeyFile(st *authState, path string, explicit *string, method string, mustExist bool) *trackedSigner {
	label := tildePath(path)
	pem, err := os.ReadFile(path)
	if err != nil {
		if mustExist || !errors.Is(err, os.ErrNotExist) {
			st.add(method, label, "not found")
		}
		return nil
	}
	signer, err := ssh.ParsePrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		pub := missing.PublicKey
		if pub == nil {
			if b, err := os.ReadFile(path + ".pub"); err == nil {
				pub, _, _, _, _ = ssh.ParseAuthorizedKey(b)
			}
		}
		if pass := m.passphraseFor(path, explicit); pass != "" {
			if s, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass)); err == nil {
				return &trackedSigner{inner: s, pub: s.PublicKey(), method: method, label: label, state: st}
			}
			st.add(method, label, "wrong passphrase")
			return nil
		}
		if pub == nil {
			st.add(method, label, "encrypted, no passphrase (no .pub file)")
			return nil
		}
		return &trackedSigner{pub: pub, method: method, label: label, lazy: &lazyKey{path: path}, state: st}
	}
	if err != nil {
		if strings.Contains(err.Error(), "sk-") || strings.Contains(err.Error(), "unsupported key type") || strings.Contains(err.Error(), "not supported") {
			st.add(method, label, "unsupported key type")
		} else {
			st.add(method, label, "error: "+err.Error())
		}
		return nil
	}
	return &trackedSigner{inner: signer, pub: signer.PublicKey(), method: method, label: label, state: st}
}

// buildAuth returns the auth methods for a target and the state that records
// what was tried. The cleanup closes the agent connection.
func (m *Manager) buildAuth(cfg model.ConnectionConfig, t model.SSHTunnel, tg sshTarget, st *authState) ([]ssh.AuthMethod, func(), error) {
	secret := func(explicit *string, kind string, has bool) (string, error) {
		if explicit != nil {
			return *explicit, nil
		}
		if cfg.ID != "" && has {
			return m.store.Secret(cfg.ID, kind)
		}
		return "", nil
	}
	switch t.AuthMethod {
	case "password":
		pw, err := secret(t.Password, store.SecretSSHPassword, t.HasPassword)
		if err != nil {
			return nil, nil, err
		}
		st.add("password", "", "rejected")
		mark := func() {
			st.mu.Lock()
			st.used = "password"
			st.tried[len(st.tried)-1].Result = "accepted"
			st.mu.Unlock()
		}
		return []ssh.AuthMethod{
			ssh.PasswordCallback(func() (string, error) { mark(); return pw, nil }),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				mark()
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}),
		}, func() {}, nil
	case "key":
		path := expandHome(t.KeyPath)
		if path == "" {
			return nil, nil, model.Errf(model.ErrInvalid, "SSH key file is required")
		}
		var explicit *string
		if t.KeyPassphrase != nil {
			explicit = t.KeyPassphrase
		} else if cfg.ID != "" && t.HasKeyPassphrase {
			if v, err := m.store.Secret(cfg.ID, store.SecretSSHPassphrase); err == nil {
				explicit = &v
			}
		}
		s := m.loadKeyFile(st, path, explicit, "key", true)
		if s == nil {
			return nil, func() {}, nil // reported via tried
		}
		st.add("key", s.label, "rejected")
		s.attempt = len(st.tried) - 1
		st.signers = append(st.signers, s)
		return []ssh.AuthMethod{ssh.PublicKeys(s)}, func() {}, nil
	}

	// auto: agent, IdentityFile entries, default key files (OpenSSH order)
	cleanup := func() {}
	var all []*trackedSigner
	seen := map[string]bool{}
	var agentSigners []ssh.Signer
	sock, why := agentSocket(tg)
	if sock != "" {
		c, err := net.DialTimeout("unix", sock, 3*time.Second)
		if err != nil {
			st.add("agent", tildePath(sock), "unavailable: "+err.Error())
		} else {
			cleanup = func() { c.Close() }
			agentSigners, err = agent.NewClient(c).Signers()
			if err != nil {
				st.add("agent", tildePath(sock), "error: "+err.Error())
			} else {
				st.agentOK = true
				st.agentKeys = len(agentSigners)
				if len(agentSigners) == 0 {
					st.add("agent", "", "no keys")
				}
			}
		}
	} else {
		st.add("agent", "", "unavailable: "+why)
	}
	// identity files: config IdentityFile, then defaults unless IdentitiesOnly
	var files []string
	for _, f := range tg.conf.IdentityFiles {
		files = append(files, expandSSHTokens(f, tg.host, tg.user))
	}
	nConfig := len(files)
	if !tg.conf.IdentitiesOnly {
		home, _ := os.UserHomeDir()
		for _, f := range defaultIdentityFiles {
			files = append(files, filepath.Join(home, ".ssh", f))
		}
	}
	var fileSigners []*trackedSigner
	filePubs := map[string]bool{}
	dupFile := map[string]bool{}
	for i, f := range files {
		if dupFile[f] {
			continue
		}
		dupFile[f] = true
		if s := m.loadKeyFile(st, f, nil, "key", i < nConfig); s != nil {
			fileSigners = append(fileSigners, s)
			filePubs[string(s.pub.Marshal())] = true
		}
	}
	// agent keys first; with IdentitiesOnly only those matching an identity file
	if st.agentOK {
		for _, as := range agentSigners {
			k := string(as.PublicKey().Marshal())
			if tg.conf.IdentitiesOnly && !filePubs[k] {
				continue
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			all = append(all, &trackedSigner{inner: as, pub: as.PublicKey(), method: "agent", label: keyLabel(as.PublicKey()), state: st})
		}
	}
	for _, s := range fileSigners {
		k := string(s.pub.Marshal())
		if seen[k] {
			continue // the agent already holds this key (also covers encrypted files)
		}
		seen[k] = true
		all = append(all, s)
	}
	signers := make([]ssh.Signer, 0, len(all))
	for _, s := range all {
		st.add(s.method, s.label, "rejected")
		s.attempt = len(st.tried) - 1
		if s.lazy != nil {
			st.tried[s.attempt].Result = "encrypted, no passphrase"
		}
		signers = append(signers, s)
	}
	st.signers = all
	if len(signers) == 0 {
		return nil, cleanup, nil
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, cleanup, nil
}

// finishTried fixes up results after the handshake: keys after the accepted one
// were not offered.
func (st *authState) finishTried(success bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !success {
		return
	}
	after := false
	for _, s := range st.signers {
		if s.signed {
			after = true
			continue
		}
		if after {
			st.tried[s.attempt].Result = "not tried"
		}
	}
}

func (st *authState) summary() string {
	var parts []string
	agentNoted := false
	for _, a := range st.tried {
		switch {
		case a.Method == "agent" && a.Key == "" && a.Result == "no keys":
			parts = append(parts, "agent (0 keys)")
			agentNoted = true
		case a.Method == "agent" && strings.HasPrefix(a.Result, "unavailable"):
			parts = append(parts, "agent ("+a.Result+")")
			agentNoted = true
		case a.Method == "password":
			parts = append(parts, "password ("+a.Result+")")
		default:
			name := a.Key
			if a.Method == "agent" {
				name = "agent key " + a.Key
			}
			parts = append(parts, name+" ("+a.Result+")")
		}
	}
	if !agentNoted && st.agentOK && st.agentKeys > 0 {
		parts = append([]string{fmt.Sprintf("agent (%d keys)", st.agentKeys)}, parts...)
	}
	if len(parts) == 0 {
		return "no authentication methods available"
	}
	return "Tried " + strings.Join(parts, ", ")
}

// sshLogin performs dial + handshake + auth for one hop. via dials through a
// previous hop (ProxyJump) when set.
func (m *Manager) sshLogin(ctx context.Context, cfg model.ConnectionConfig, t model.SSHTunnel, tg sshTarget, via *ssh.Client, st *authState, hostKey *string) (*ssh.Client, error) {
	auth, cleanup, err := m.buildAuth(cfg, t, tg, st)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if len(auth) == 0 {
		return nil, m.authError(cfg, tg, st, nil)
	}
	cb := m.hostKeyCallback(cfg.ID)
	conf := &ssh.ClientConfig{
		User: tg.user,
		Auth: auth,
		HostKeyCallback: func(h string, r net.Addr, k ssh.PublicKey) error {
			if hostKey != nil {
				*hostKey = k.Type() + " " + ssh.FingerprintSHA256(k)
			}
			return cb(h, r, k)
		},
		Timeout: 10 * time.Second,
	}
	var raw net.Conn
	if via != nil {
		raw, err = via.DialContext(ctx, "tcp", tg.addr)
	} else {
		d := net.Dialer{Timeout: 10 * time.Second}
		raw, err = d.DialContext(ctx, "tcp", tg.addr)
	}
	if err != nil {
		return nil, model.Errf(model.ErrSSH, "SSH connect to %s: %v", tg.addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	remote := raw.RemoteAddr()
	if via != nil {
		if a, err := net.ResolveTCPAddr("tcp", tg.addr); err == nil {
			remote = a
		}
	}
	if algos := m.knownKeyAlgorithms(tg.addr, remote); len(algos) > 0 {
		conf.HostKeyAlgorithms = algos
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, tg.addr, conf)
	if err != nil {
		raw.Close()
		st.finishTried(false)
		var ae *model.AppError
		if errors.As(err, &ae) {
			return nil, ae // host key errors
		}
		if st.needPass != nil || errors.Is(err, errNeedPassphrase) || strings.Contains(err.Error(), "unable to authenticate") {
			return nil, m.authError(cfg, tg, st, err)
		}
		return nil, model.Errf(model.ErrSSH, "SSH handshake with %s: %v", tg.addr, err)
	}
	_ = raw.SetDeadline(time.Time{})
	st.finishTried(true)
	return ssh.NewClient(c, chans, reqs), nil
}

func (m *Manager) authError(cfg model.ConnectionConfig, tg sshTarget, st *authState, err error) error {
	if st.needPass != nil {
		p := *st.needPass
		p.ConnID = cfg.ID
		if m.OnKeyPassphrase != nil {
			m.OnKeyPassphrase(p)
		}
		return model.Errf(model.ErrPassphraseRequired, "SSH key %s is encrypted: enter its passphrase (%s)", tildePath(p.KeyPath), st.summary())
	}
	msg := fmt.Sprintf("SSH authentication failed for %s@%s. %s.", tg.user, tg.addr, st.summary())
	if st.agentOK && st.agentKeys == 0 {
		msg += " The SSH agent has no keys loaded; load yours with: ssh-add --apple-use-keychain ~/.ssh/id_ed25519"
	}
	return model.Errf(model.ErrSSH, "%s", msg)
}

// sshSession is an established tunnel with what was learned on the way.
type sshSession struct {
	client  *ssh.Client
	jump    *ssh.Client
	target  sshTarget
	state   *authState
	hostKey string
}

func (s *sshSession) Close() {
	s.client.Close()
	if s.jump != nil {
		s.jump.Close()
	}
}

// openSSH resolves the target (~/.ssh/config), handles one ProxyJump hop and logs in.
func (m *Manager) openSSH(ctx context.Context, cfg model.ConnectionConfig) (*sshSession, error) {
	t := cfg.SSH
	if t.Host == "" {
		return nil, model.Errf(model.ErrInvalid, "SSH host is required")
	}
	tg := resolveSSHTarget(t)
	sess := &sshSession{target: tg, state: &authState{}}
	var via *ssh.Client
	if pj := tg.proxyJump; pj != "" && !strings.EqualFold(pj, "none") {
		if strings.Contains(pj, ",") {
			return nil, model.Errf(model.ErrSSH, "ProxyJump with more than one hop (%s) is not supported", pj)
		}
		jt := model.SSHTunnel{AuthMethod: "auto"}
		hostPort := pj
		if strings.HasPrefix(hostPort, "ssh://") {
			hostPort = strings.TrimPrefix(hostPort, "ssh://")
		}
		if at := strings.LastIndex(hostPort, "@"); at >= 0 {
			jt.User, hostPort = hostPort[:at], hostPort[at+1:]
		}
		if h, p, err := net.SplitHostPort(hostPort); err == nil {
			jt.Host = h
			jt.Port, _ = strconv.Atoi(p)
		} else {
			jt.Host = hostPort
		}
		jtg := resolveSSHTarget(jt)
		if jtg.proxyJump != "" && !strings.EqualFold(jtg.proxyJump, "none") {
			return nil, model.Errf(model.ErrSSH, "nested ProxyJump (%s via %s) is not supported", pj, jtg.proxyJump)
		}
		jst := &authState{}
		jc, err := m.sshLogin(ctx, cfg, jt, jtg, nil, jst, nil)
		if err != nil {
			var ae *model.AppError
			if errors.As(err, &ae) {
				return nil, model.Errf(ae.Code, "jump host %s: %s", jtg.addr, ae.Message)
			}
			return nil, err
		}
		sess.jump, via = jc, jc
	}
	c, err := m.sshLogin(ctx, cfg, t, tg, via, sess.state, &sess.hostKey)
	if err != nil {
		if sess.jump != nil {
			sess.jump.Close()
		}
		return nil, err
	}
	sess.client = c
	return sess, nil
}

// dialSSH opens the SSH client for a connection's tunnel.
func (m *Manager) dialSSH(ctx context.Context, cfg model.ConnectionConfig, onLost func()) (*ssh.Client, error) {
	sess, err := m.openSSH(ctx, cfg)
	if err != nil {
		return nil, err
	}
	client := sess.client
	go func() { // keepalive; report a lost tunnel so the pool is dropped
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		done := make(chan struct{})
		go func() { client.Wait(); close(done) }()
		for {
			select {
			case <-done:
				if sess.jump != nil {
					sess.jump.Close()
				}
				if onLost != nil {
					onLost()
				}
				return
			case <-tk.C:
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					client.Close()
				}
			}
		}
	}()
	return client, nil
}

// dbEndpoint returns the database network/address as seen from the SSH host.
func dbEndpoint(cfg model.ConnectionConfig) (string, string) {
	port := cfg.Port
	if cfg.Socket != "" {
		if cfg.Driver == model.DriverPostgres {
			if port == 0 {
				port = 5432
			}
			return "unix", strings.TrimRight(cfg.Socket, "/") + "/.s.PGSQL." + strconv.Itoa(port)
		}
		return "unix", cfg.Socket
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 3306
		if cfg.Driver == model.DriverPostgres {
			port = 5432
		}
	}
	return "tcp", net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// TestSSH logs in through the tunnel (same auth chain, host key and passphrase
// flows as Connect) and opens a direct channel to the database endpoint, without
// any database protocol.
func (m *Manager) TestSSH(ctx context.Context, cfg model.ConnectionConfig) (model.SSHTestResult, error) {
	start := time.Now()
	if err := store.Normalize(&cfg); err != nil {
		return model.SSHTestResult{}, err
	}
	if cfg.ID != "" && cfg.SSH.Password == nil && cfg.SSH.KeyPassphrase == nil {
		if old, err := m.store.Connection(cfg.ID); err == nil {
			cfg.SSH.HasPassword, cfg.SSH.HasKeyPassphrase = old.SSH.HasPassword, old.SSH.HasKeyPassphrase
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sess, err := m.openSSH(ctx, cfg)
	if err != nil {
		return model.SSHTestResult{}, err
	}
	defer sess.Close()
	res := model.SSHTestResult{
		OK: true, AuthMethod: sess.state.used, User: sess.target.user, Address: sess.target.addr,
		ServerVersion: string(sess.client.ServerVersion()), HostKeyFingerprint: sess.hostKey,
		Tried: sess.state.tried,
	}
	if sess.jump != nil {
		res.ProxyJump = sess.target.proxyJump
	}
	network, addr := dbEndpoint(cfg)
	res.DBEndpoint = addr
	if c, err := sess.client.DialContext(ctx, network, addr); err != nil {
		res.DBError = err.Error()
	} else {
		c.Close()
		res.DBReachable = true
	}
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}
