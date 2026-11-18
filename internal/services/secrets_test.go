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

	// legacy: hasPassword set (e.g. saved to the Keychain by an older build) but
	// nothing in the file: the flag is reported (and stored) as false
	if err := os.WriteFile(sf, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.cs.GetConnection(c.ID); got.HasPassword {
		t.Fatal("stale hasPassword reported as true (GetConnection)")
	}
	if list, _ := f.cs.Connections(""); len(list) != 1 || list[0].HasPassword {
		t.Fatalf("stale hasPassword reported as true (Connections): %+v", list)
	}
	if _, err := f.cs.Connect(f.ctx, c.ID, ""); err == nil || !strings.HasPrefix(err.Error(), model.ErrPasswordNeeded) {
		t.Fatalf("expected password_required, got %v", err)
	}
	// a wrong password at the prompt is not persisted
	if _, err := f.cs.Connect(f.ctx, c.ID, "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if b, _ := os.ReadFile(sf); strings.Contains(string(b), "wrong-password") {
		t.Fatal("wrong password persisted")
	}
	// the right one is persisted after the successful connect
	if _, err := f.cs.Connect(f.ctx, c.ID, "dbviewer"); err != nil {
		t.Fatalf("connect with entered password: %v", err)
	}
	if got, _ := f.cs.GetConnection(c.ID); !got.HasPassword {
		t.Fatal("hasPassword not set after a successful connect with a password")
	}
	f.cs.Disconnect(c.ID)
	if _, err := f.cs.Connect(f.ctx, c.ID, ""); err != nil {
		t.Fatalf("next connect without a password: %v", err)
	}
	// "ask" connections never persist
	ask := c
	ask.ID, ask.Name, ask.PasswordMode, ask.Password = "", "ask", model.PasswordAsk, nil
	ac, err := f.cs.SaveConnection(ask)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.Connect(f.ctx, ac.ID, "dbviewer"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.cs.GetConnection(ac.ID); got.HasPassword {
		t.Fatal("ask-mode password persisted")
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

// An "ask" connection tried without a password (the UI does so for local
// databases) reports password_required when the server wants one.
func TestAskModeEmptyPassword(t *testing.T) {
	f := newFixture(t)
	c := f.save(model.ConnectionConfig{Name: "pg-ask", Driver: model.DriverPostgres, Host: "127.0.0.1", Port: 55432, User: "postgres",
		PasswordMode: model.PasswordAsk, Database: "dbviewer_dummy", SSLMode: model.SSLDisable})
	if _, err := f.cs.Connect(f.ctx, c.ID, ""); err == nil || !strings.HasPrefix(err.Error(), model.ErrPasswordNeeded) {
		t.Fatalf("expected password_required, got %v", err)
	}
	if _, err := f.cs.Connect(f.ctx, c.ID, "dbviewer"); err != nil {
		t.Fatalf("connect with entered password: %v", err)
	}
	f.cs.Disconnect(c.ID)
}
