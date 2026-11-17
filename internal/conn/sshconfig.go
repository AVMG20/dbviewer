package conn

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// sshHostConfig is the subset of ~/.ssh/config we honour for one host.
type sshHostConfig struct {
	HostName       string
	User           string
	Port           int
	IdentityFiles  []string
	IdentitiesOnly bool
	IdentityAgent  string // "" = not set, "none" = disabled
	ProxyJump      string
}

type sshConfigBlock struct {
	patterns []string // Host patterns; nil = global (before the first Host)
	opts     [][2]string
}

// parseSSHConfig reads an OpenSSH client config (Host blocks, Include). Match
// blocks are skipped (their options are ignored).
func parseSSHConfig(path string, depth int) []sshConfigBlock {
	f, err := os.Open(path)
	if err != nil || depth > 5 {
		return nil
	}
	defer f.Close()
	var blocks []sshConfigBlock
	cur := sshConfigBlock{}
	skipping := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val := splitConfigLine(line)
		if key == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			blocks = append(blocks, cur)
			cur = sshConfigBlock{patterns: splitArgs(val)}
			skipping = false
			continue
		case "match":
			blocks = append(blocks, cur)
			cur = sshConfigBlock{}
			skipping = true
			continue
		case "include":
			if skipping {
				continue
			}
			for _, pat := range splitArgs(val) {
				pat = expandHome(pat)
				if !filepath.IsAbs(pat) {
					pat = filepath.Join(filepath.Dir(path), pat)
				}
				matches, _ := filepath.Glob(pat)
				for _, m := range matches {
					for _, b := range parseSSHConfig(m, depth+1) {
						if b.patterns == nil && cur.patterns != nil {
							b.patterns = cur.patterns // included lines belong to the enclosing Host
						}
						blocks = append(blocks, b)
					}
				}
			}
			continue
		}
		if !skipping {
			cur.opts = append(cur.opts, [2]string{strings.ToLower(key), val})
		}
	}
	return append(blocks, cur)
}

func splitConfigLine(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, ""
	}
	key := line[:i]
	val := strings.TrimLeft(line[i:], " \t")
	val = strings.TrimPrefix(val, "=")
	return key, strings.TrimSpace(val)
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case (r == ' ' || r == '\t') && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// matchPattern implements OpenSSH host patterns (* and ?).
func matchPattern(pat, host string) bool {
	m, err := filepath.Match(strings.ToLower(pat), strings.ToLower(host))
	return err == nil && m
}

func blockMatches(b sshConfigBlock, host string) bool {
	if b.patterns == nil {
		return true
	}
	matched := false
	for _, p := range b.patterns {
		if strings.HasPrefix(p, "!") {
			if matchPattern(p[1:], host) {
				return false
			}
			continue
		}
		if matchPattern(p, host) {
			matched = true
		}
	}
	return matched
}

// resolveSSHConfig returns the settings for host from ~/.ssh/config: the first
// value of each option wins (OpenSSH semantics), IdentityFile accumulates.
func resolveSSHConfig(host string) sshHostConfig {
	var hc sshHostConfig
	home, err := os.UserHomeDir()
	if err != nil {
		return hc
	}
	seen := map[string]bool{}
	for _, b := range parseSSHConfig(filepath.Join(home, ".ssh", "config"), 0) {
		if !blockMatches(b, host) {
			continue
		}
		for _, o := range b.opts {
			k, v := o[0], strings.Trim(o[1], `"`)
			if k == "identityfile" {
				hc.IdentityFiles = append(hc.IdentityFiles, v)
				continue
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			switch k {
			case "hostname":
				hc.HostName = v
			case "user":
				hc.User = v
			case "port":
				hc.Port, _ = strconv.Atoi(v)
			case "identitiesonly":
				hc.IdentitiesOnly = strings.EqualFold(v, "yes")
			case "identityagent":
				hc.IdentityAgent = v
			case "proxyjump":
				hc.ProxyJump = v
			}
		}
	}
	return hc
}

// expandSSHTokens expands ~ and the common % tokens in IdentityFile/IdentityAgent.
func expandSSHTokens(s, host, remoteUser string) string {
	home, _ := os.UserHomeDir()
	local := osUser()
	s = strings.NewReplacer("%d", home, "%h", host, "%r", remoteUser, "%u", local, "%%", "%").Replace(s)
	return expandHome(s)
}

func osUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}
