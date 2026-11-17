package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	secretPassword      = "password"
	secretSSHPassword   = "ssh-password"
	secretSSHPassphrase = "ssh-passphrase"
)

// Secret kinds accepted by Store.Secret.
const (
	SecretPassword      = secretPassword
	SecretSSHPassword   = secretSSHPassword
	SecretSSHPassphrase = secretSSHPassphrase
)

func secretKey(connID, kind string) string { return "conn:" + connID + ":" + kind }

// Secrets stores connection secrets. Get returns ok=false when absent.
type Secrets interface {
	Get(key string) (string, bool, error)
	Set(key, value string) error
	Delete(key string) error
}

// newSecrets returns the secrets store: a JSON file (secrets.json, mode 0600) in
// the data dir (mode 0700). The macOS Keychain is deliberately not used: an
// ad-hoc signed app gets a Keychain prompt on every access, and the Keychain is
// never read (not even to migrate), so no prompt can appear.
func newSecrets(dir string) Secrets {
	return &fileSecrets{path: filepath.Join(dir, "secrets.json")}
}

type fileSecrets struct {
	path string
	mu   sync.Mutex
}

func (f *fileSecrets) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return m, nil
	}
	return m, json.Unmarshal(b, &m)
}

// save writes atomically: temp file (0600) in the same dir, fsync, rename.
func (f *fileSecrets) save(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".secrets-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, f.path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (f *fileSecrets) Get(key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", false, err
	}
	v, ok := m[key]
	return v, ok, nil
}

func (f *fileSecrets) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

func (f *fileSecrets) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return f.save(m)
}
