package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dbviewer/internal/model"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// pendingKeys holds host keys seen on first contact, waiting for TrustHostKey.
type pendingKeys struct {
	mu   sync.Mutex
	keys map[string]ssh.PublicKey // "host:port|fingerprint" -> key
}

func (m *Manager) knownHostsFile() string { return filepath.Join(m.store.Dir(), "known_hosts") }

func knownHostsFiles(own string) []string {
	files := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".ssh", "known_hosts")
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	if _, err := os.Stat(own); err != nil {
		_ = os.WriteFile(own, nil, 0o600)
	}
	return append(files, own)
}

func (m *Manager) hostKeyCallback(connID string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		cb, err := knownhosts.New(knownHostsFiles(m.knownHostsFile())...)
		if err != nil {
			return model.Errf(model.ErrSSH, "reading known_hosts: %v", err)
		}
		err = cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			fp := ssh.FingerprintSHA256(key)
			if len(ke.Want) > 0 {
				return model.Errf(model.ErrHostKeyMismatch,
					"host key for %s changed (got %s %s). This may be an attack; fix known_hosts if the change is expected", hostname, key.Type(), fp)
			}
			m.pending.mu.Lock()
			m.pending.keys[hostname+"|"+fp] = key
			m.pending.mu.Unlock()
			if m.OnHostKey != nil {
				m.OnHostKey(model.HostKeyPrompt{ConnID: connID, Host: hostname, KeyType: key.Type(), Fingerprint: fp})
			}
			return model.Errf(model.ErrHostKeyUnknown, "unknown SSH host key for %s: %s %s (trust it with TrustHostKey)", hostname, key.Type(), fp)
		}
		return err
	}
}

// TrustHostKey adds a host key seen during a failed connect (host_key_unknown)
// to the app's known_hosts file. host is "host:port" as reported in the prompt.
func (m *Manager) TrustHostKey(host, fingerprint string) error {
	m.pending.mu.Lock()
	key := m.pending.keys[host+"|"+fingerprint]
	delete(m.pending.keys, host+"|"+fingerprint)
	m.pending.mu.Unlock()
	if key == nil {
		return model.Errf(model.ErrNotFound, "no pending host key %s for %s; connect again first", fingerprint, host)
	}
	f, err := os.OpenFile(m.knownHostsFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(host)}, key) + "\n")
	return err
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func sshDialer(client *ssh.Client) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := client.DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("via SSH tunnel: %w", err)
		}
		return c, nil
	}
}

var probeKey = func() ssh.PublicKey {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := ssh.NewSignerFromKey(priv)
	return s.PublicKey()
}()

// knownKeyAlgorithms returns the host key algorithms matching the key types
// recorded for host in known_hosts (nil if the host is unknown).
func (m *Manager) knownKeyAlgorithms(host string, remote net.Addr) []string {
	cb, err := knownhosts.New(knownHostsFiles(m.knownHostsFile())...)
	if err != nil {
		return nil
	}
	var ke *knownhosts.KeyError
	if err := cb(host, remote, probeKey); !errors.As(err, &ke) || len(ke.Want) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	for _, k := range ke.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return out
}
