package conn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ssh", "conf.d"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "conf.d", "work"), []byte("Host *.example.net\n  User app\n  IdentityFile ~/.ssh/work_key\n"), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(`# comment
Include conf.d/*

Host db1 db1alias
    HostName db1.example.net
    Port=2222
    IdentityAgent "~/Library/Group Containers/agent.sock"

Host *.example.net !secret.example.net
    IdentitiesOnly yes
    User ignored-second-value

Match host foo
    User matchuser

Host *
    User fallback
    IdentityFile ~/.ssh/id_default
    ProxyJump none
`), 0o600)
	hc := resolveSSHConfig("db1")
	if hc.HostName != "db1.example.net" || hc.Port != 2222 || hc.User != "fallback" || hc.IdentityAgent != "~/Library/Group Containers/agent.sock" {
		t.Fatalf("alias: %+v", hc)
	}
	hc = resolveSSHConfig("db1.example.net")
	if hc.User != "app" || !hc.IdentitiesOnly || len(hc.IdentityFiles) != 2 || hc.IdentityFiles[0] != "~/.ssh/work_key" {
		t.Fatalf("wildcard + include: %+v", hc)
	}
	if hc = resolveSSHConfig("secret.example.net"); hc.IdentitiesOnly || hc.User != "app" {
		t.Fatalf("negation: %+v", hc)
	}
	if got := expandSSHTokens("~/.ssh/%h_key", "db", "app"); got != filepath.Join(home, ".ssh", "db_key") {
		t.Fatalf("tokens: %s", got)
	}
}
