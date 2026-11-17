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
	os.WriteFile(filepath.Join(home, ".ssh", "conf.d", "work"), []byte("Host *.hypernode.io\n  User app\n  IdentityFile ~/.ssh/work_key\n"), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(`# comment
Include conf.d/*

Host sk skservice
    HostName skservicedev.hypernode.io
    Port=2222
    IdentityAgent "~/Library/Group Containers/agent.sock"

Host *.hypernode.io !secret.hypernode.io
    IdentitiesOnly yes
    User ignored-second-value

Match host foo
    User matchuser

Host *
    User fallback
    IdentityFile ~/.ssh/id_default
    ProxyJump none
`), 0o600)
	hc := resolveSSHConfig("sk")
	if hc.HostName != "skservicedev.hypernode.io" || hc.Port != 2222 || hc.User != "fallback" || hc.IdentityAgent != "~/Library/Group Containers/agent.sock" {
		t.Fatalf("alias: %+v", hc)
	}
	hc = resolveSSHConfig("skservicedev.hypernode.io")
	if hc.User != "app" || !hc.IdentitiesOnly || len(hc.IdentityFiles) != 2 || hc.IdentityFiles[0] != "~/.ssh/work_key" {
		t.Fatalf("wildcard + include: %+v", hc)
	}
	if hc = resolveSSHConfig("secret.hypernode.io"); hc.IdentitiesOnly || hc.User != "app" {
		t.Fatalf("negation: %+v", hc)
	}
	if got := expandSSHTokens("~/.ssh/%h_key", "db", "app"); got != filepath.Join(home, ".ssh", "db_key") {
		t.Fatalf("tokens: %s", got)
	}
}
