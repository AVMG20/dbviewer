package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbviewer/internal/model"
)

// Secrets live in secrets.json (0600) in the data dir (0700); no Keychain. A
// password that is not in the file (e.g. it used to be in the Keychain) makes
// Connect ask for it (password_required) and accept it as an argument.
func TestSecretsFileOnly(t *testing.T) {
	f := newFixture(t)
	pw := "dbviewer"
	c := f.save(model.ConnectionConfig{Name: "pg", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres",
		Password: &pw, Database: "dbviewer_dummy", SSLMode: model.SSLDisable})
	if !c.HasPassword {
		t.Fatal("password not saved")
	}
	dir := f.core.Store.Dir()
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode: %v %v", st.Mode().Perm(), err)
	}
	sf := filepath.Join(dir, "secrets.json")
	st, err := os.Stat(sf)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json mode: %v %v", st, err)
	}
	if b, _ := os.ReadFile(sf); !strings.Contains(string(b), "dbviewer") {
		t.Fatal("password not in secrets.json")
	}
	if _, err := f.cs.Connect(f.ctx, c.ID, ""); err != nil {
		t.Fatalf("connect with saved password: %v", err)
	}
	f.cs.Disconnect(c.ID)

	// legacy: hasPassword set but nothing in the file
	if err := os.WriteFile(sf, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.Connect(f.ctx, c.ID, ""); err == nil || !strings.HasPrefix(err.Error(), model.ErrPasswordNeeded) {
		t.Fatalf("expected password_required, got %v", err)
	}
	if _, err := f.cs.Connect(f.ctx, c.ID, "dbviewer"); err != nil {
		t.Fatalf("connect with entered password: %v", err)
	}
	// saving again stores it in the file
	c.Password = &pw
	if _, err := f.cs.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(sf); !strings.Contains(string(b), "dbviewer") {
		t.Fatal("password not saved again")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
