// Package envimport turns a Laravel-style .env file into a ConnectionConfig.
package envimport

import (
	"bufio"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"dbviewer/internal/model"
)

// ParseFile reads and parses an .env file. Relative SQLite paths are resolved
// against the file's directory.
func ParseFile(path string) (model.EnvImportResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.EnvImportResult{}, model.Wrap(model.ErrInvalid, err)
	}
	res := Parse(string(b), filepath.Dir(path))
	res.SourcePath = path
	return res, nil
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Vars parses KEY=VALUE lines. Comments (#), blank lines, a UTF-8 BOM and
// `export ` prefixes are handled; quoted values are unquoted (the first unescaped
// closing quote ends the value, a trailing " # comment" is dropped); ${VAR}
// references are expanded in unquoted and double-quoted values; later
// definitions win.
func Vars(text string) map[string]string {
	v, _ := parseVars(text)
	return v
}

func parseVars(text string) (map[string]string, []string) {
	vars := map[string]string{}
	var warnings []string
	text = strings.TrimPrefix(text, "\ufeff")
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(v, `"`):
			val, _ := scanQuoted(v[1:], '"', true)
			v = expandWarn(val, vars, k, &warnings)
		case strings.HasPrefix(v, "'"):
			v, _ = scanQuoted(v[1:], '\'', false)
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			v = expandWarn(v, vars, k, &warnings)
		}
		vars[k] = v
	}
	return vars, warnings
}

// scanQuoted reads a quoted value up to the first unescaped closing quote.
// Double quotes support \n \" \\ escapes; single quotes are literal (” = ').
func scanQuoted(s string, q byte, escapes bool) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escapes && c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		case c == q:
			if !escapes && i+1 < len(s) && s[i+1] == q {
				b.WriteByte(q)
				i++
				continue
			}
			return b.String(), true
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), false
}

func expandWarn(v string, vars map[string]string, key string, warnings *[]string) string {
	return varRef.ReplaceAllStringFunc(v, func(m string) string {
		name := m[2 : len(m)-1]
		val, ok := vars[name]
		if !ok {
			*warnings = append(*warnings, key+": ${"+name+"} is not defined (expanded to an empty string)")
		}
		return val
	})
}

// nullish reports Laravel's special values that mean "not set".
func nullish(v string) bool {
	switch strings.ToLower(v) {
	case "null", "(null)", "empty", "(empty)":
		return true
	}
	return false
}

var dbKeys = []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_SOCKET", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL", "DB_URL"}

// Parse builds a prefilled ConnectionConfig from .env text. baseDir resolves
// relative SQLite paths ("" = leave as is).
func Parse(text, baseDir string) model.EnvImportResult {
	vars, warnings := parseVars(text)
	for k, v := range vars {
		if nullish(v) {
			vars[k] = "" // Laravel reads null/(null)/empty as "not set"
		}
	}
	res := model.EnvImportResult{Found: map[string]string{}, Warnings: append([]string{}, warnings...)}
	for _, k := range append(dbKeys, "APP_NAME", "APP_ENV") {
		if v, ok := vars[k]; ok {
			switch {
			case k == "DB_PASSWORD" && v != "":
				v = "••••"
			case k == "DATABASE_URL" || k == "DB_URL":
				v = maskURL(v)
			}
			res.Found[k] = v
		}
	}
	c := model.ConnectionConfig{PasswordMode: model.PasswordSave, Color: model.ColorNone, Params: map[string]string{}}

	// Laravel: a database URL wins over the individual DB_* keys; those only fill
	// what the URL leaves empty.
	fromURL := false
	if u := firstNonEmpty(vars["DATABASE_URL"], vars["DB_URL"]); u != "" {
		if err := applyURL(&c, u); err != nil {
			res.Warnings = append(res.Warnings, "DATABASE_URL ignored: "+err.Error())
		} else {
			fromURL = true
			var ignored []string
			for _, k := range []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DB_SOCKET"} {
				if v := vars[k]; v != "" && !urlLeftEmpty(c, k) {
					ignored = append(ignored, k)
				}
			}
			if len(ignored) > 0 {
				res.Warnings = append(res.Warnings, "DATABASE_URL takes precedence; ignored "+strings.Join(ignored, ", "))
			}
		}
	}
	set := func(key string, apply func(v string)) {
		v, ok := vars[key]
		if !ok || v == "" {
			return
		}
		if fromURL && !urlLeftEmpty(c, key) {
			return
		}
		apply(v)
	}
	if c.Driver == "" {
		switch strings.ToLower(vars["DB_CONNECTION"]) {
		case "pgsql", "postgres", "postgresql":
			c.Driver = model.DriverPostgres
		case "mysql", "mariadb":
			c.Driver = model.DriverMySQL
		case "sqlite":
			c.Driver = model.DriverSQLite
		case "":
			if _, ok := vars["DB_HOST"]; ok {
				c.Driver = model.DriverMySQL
				res.Warnings = append(res.Warnings, "DB_CONNECTION missing, assuming mysql")
			}
		default:
			res.Warnings = append(res.Warnings, "unsupported DB_CONNECTION "+vars["DB_CONNECTION"])
		}
	}
	set("DB_HOST", func(v string) { c.Host = v })
	set("DB_PORT", func(v string) {
		if p, err := strconv.Atoi(v); err == nil {
			c.Port = p
		} else {
			res.Warnings = append(res.Warnings, "invalid DB_PORT "+v)
		}
	})
	set("DB_SOCKET", func(v string) { c.Socket = v })
	set("DB_USERNAME", func(v string) { c.User = v })
	if v, ok := vars["DB_PASSWORD"]; ok && (!fromURL || c.Password == nil) {
		pw := v
		c.Password = &pw
	}
	set("DB_DATABASE", func(v string) {
		if c.Driver == model.DriverSQLite {
			c.FilePath = v
		} else {
			c.Database = v
		}
	})
	if c.Driver == model.DriverSQLite {
		if c.FilePath == "" {
			c.FilePath = "database/database.sqlite" // Laravel default
		}
		if c.FilePath == ":memory:" {
			res.Warnings = append(res.Warnings, "in-memory SQLite database (:memory:) cannot be opened from outside the app; pick the database file")
			c.FilePath = ""
		} else if baseDir != "" && !filepath.IsAbs(c.FilePath) {
			c.FilePath = filepath.Join(baseDir, c.FilePath)
		}
		c.Host, c.Port, c.Socket, c.User, c.Password = "", 0, "", "", nil
	}
	if c.Socket != "" && (c.Host == "127.0.0.1" || c.Host == "localhost") {
		res.Warnings = append(res.Warnings, "both DB_HOST and DB_SOCKET set; the socket is used")
	}
	if c.Driver == model.DriverPostgres && c.Database == "" {
		c.Database = "postgres"
	}

	switch {
	case vars["APP_NAME"] != "" && c.Database != "":
		c.Name = vars["APP_NAME"] + " (" + c.Database + ")"
	case c.Database != "":
		c.Name = c.Database
	case c.FilePath != "":
		c.Name = filepath.Base(c.FilePath)
	}
	if env := strings.ToLower(vars["APP_ENV"]); env == "production" || env == "prod" {
		c.Color = model.ColorRed
		c.ReadOnly = true
		res.Warnings = append(res.Warnings, "APP_ENV=production: color set to red and read-only enabled")
	}
	if c.Driver == "" {
		res.Warnings = append(res.Warnings, "no database settings found")
	}
	res.Config = c
	return res
}

func applyURL(c *model.ConnectionConfig, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql", "pgsql":
		c.Driver = model.DriverPostgres
	case "mysql", "mariadb":
		c.Driver = model.DriverMySQL
	case "sqlite", "sqlite3":
		c.Driver = model.DriverSQLite
		switch {
		case u.Opaque != "": // sqlite:database/app.sqlite
			c.FilePath = u.Opaque
		case u.Host != "": // sqlite://relative/path
			c.FilePath = u.Host + u.Path
		default: // sqlite:///abs/path
			c.FilePath = u.Path
		}
		return nil
	default:
		return model.Errf(model.ErrInvalid, "unknown scheme %q", u.Scheme)
	}
	c.Host = u.Hostname()
	if p := u.Port(); p != "" {
		c.Port, _ = strconv.Atoi(p)
	}
	if u.User != nil {
		c.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			c.Password = &pw
		}
	}
	c.Database = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	if v := q.Get("sslmode"); v != "" {
		c.SSLMode = v
	}
	if v := q.Get("unix_socket"); v != "" {
		c.Socket = v
	}
	if v := q.Get("host"); v != "" && strings.HasPrefix(v, "/") { // postgres socket dir
		c.Socket = v
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// urlLeftEmpty reports whether the database URL left the field of a DB_* key empty.
func urlLeftEmpty(c model.ConnectionConfig, key string) bool {
	switch key {
	case "DB_CONNECTION":
		return c.Driver == ""
	case "DB_HOST":
		return c.Host == ""
	case "DB_PORT":
		return c.Port == 0
	case "DB_DATABASE":
		return c.Database == "" && c.FilePath == ""
	case "DB_USERNAME":
		return c.User == ""
	case "DB_PASSWORD":
		return c.Password == nil
	case "DB_SOCKET":
		return c.Socket == ""
	}
	return true
}

// maskURL hides the password of a database URL.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		if i := strings.Index(raw, "@"); i >= 0 {
			if j := strings.Index(raw, "://"); j >= 0 && j < i {
				if k := strings.Index(raw[j+3:i], ":"); k >= 0 {
					return raw[:j+3+k+1] + "••••" + raw[i:]
				}
			}
		}
		return raw
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
		return strings.Replace(u.String(), ":xxxxx@", ":••••@", 1)
	}
	return raw
}
