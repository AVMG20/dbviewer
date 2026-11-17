package envimport

import (
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dbviewer/internal/model"
)

// Formats reported in EnvImportResult.Found["format"].
const (
	FormatURL      = "url"
	FormatJDBC     = "jdbc"
	FormatMySQLDSN = "mysql-dsn"
	FormatLibpq    = "libpq"
	FormatEnv      = "env"
)

var (
	envLineRe   = regexp.MustCompile(`(?m)^\s*(export\s+)?[A-Z][A-Z0-9_]*\s*=`)
	schemeRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	libpqKeyRe  = regexp.MustCompile(`^\s*[a-z_]+\s*=`)
	mysqlDSNRe  = regexp.MustCompile(`^(?:(.*)@)?(tcp|tcp6|unix)\(([^)]*)\)/([^?]*)(?:\?(.*))?$`)
	errUnknown  = "unrecognized connection string: paste a URL (mysql://, postgres://, sqlite://…), a JDBC URL, a Go MySQL DSN (user:pass@tcp(host:3306)/db), libpq key=value pairs or .env lines"
	libpqKnown  = map[string]bool{"host": true, "hostaddr": true, "port": true, "dbname": true, "user": true, "password": true, "sslmode": true, "options": true, "application_name": true, "connect_timeout": true, "sslrootcert": true, "sslcert": true, "sslkey": true, "target_session_attrs": true, "client_encoding": true, "passfile": true, "service": true}
	silentQuery = map[string]bool{"application_name": true, "parsetime": true, "loc": true, "timeout": true, "readtimeout": true, "writetimeout": true,
		"connect_timeout": true, "allownativepasswords": true, "characterencoding": true, "useunicode": true, "servertimezone": true,
		"collation": true, "interpolateparams": true, "multistatements": true, "clientfoundrows": true, "columnswithalias": true,
		"autoreconnect": true, "zerodatetimebehavior": true, "allowpublickeyretrieval": true, "target_session_attrs": true}
)

// ParseConnectionString turns a pasted connection string into a prefilled
// ConnectionConfig (not saved). It accepts database URLs, JDBC URLs, Go MySQL
// DSNs, libpq keyword/value strings and .env lines/blobs (delegated to Parse).
// The password is never put into warnings or Found.
func ParseConnectionString(text string) (model.EnvImportResult, error) {
	s := strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	s = trimQuotes(s)
	if s == "" {
		return model.EnvImportResult{}, model.Errf(model.ErrInvalid, "the connection string is empty")
	}
	res := model.EnvImportResult{Found: map[string]string{}, Warnings: []string{}}
	c := model.ConnectionConfig{PasswordMode: model.PasswordSave, Color: model.ColorNone, Params: map[string]string{}}
	var err error
	switch {
	case envLineRe.MatchString(s):
		r := Parse(text, "")
		if r.Config.Driver == "" {
			return r, model.Errf(model.ErrInvalid, "no database settings found in the pasted lines (DB_CONNECTION, DB_HOST… or DATABASE_URL)")
		}
		r.Found["format"] = FormatEnv
		return r, nil
	case strings.HasPrefix(strings.ToLower(s), "jdbc:"):
		res.Found["format"] = FormatJDBC
		err = parseURL(&c, &res, s[5:], true)
	case mysqlDSNRe.MatchString(s) && !strings.Contains(s, "://"):
		res.Found["format"] = FormatMySQLDSN
		err = parseMySQLDSN(&c, &res, s)
	case schemeRe.MatchString(s):
		res.Found["format"] = FormatURL
		err = parseURL(&c, &res, s, false)
	case libpqKeyRe.MatchString(s):
		res.Found["format"] = FormatLibpq
		err = parseLibpq(&c, &res, s)
	default:
		return res, model.Errf(model.ErrInvalid, "%s", errUnknown)
	}
	if err != nil {
		return res, err
	}
	finish(&c)
	summarize(&res, c)
	res.Config = c
	return res, nil
}

func trimQuotes(s string) string {
	for len(s) >= 2 {
		f, l := s[0], s[len(s)-1]
		if (f == '"' || f == '\'' || f == '`') && f == l {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	return s
}

// summarize lists the recognised parts in Found (password masked).
func summarize(res *model.EnvImportResult, c model.ConnectionConfig) {
	add := func(k, v string) {
		if v != "" {
			res.Found[k] = v
		}
	}
	add("driver", c.Driver)
	add("host", c.Host)
	if c.Port != 0 {
		add("port", strconv.Itoa(c.Port))
	}
	add("socket", c.Socket)
	add("user", c.User)
	if c.Password != nil {
		if *c.Password == "" {
			res.Found["password"] = ""
		} else {
			res.Found["password"] = "••••"
		}
	}
	add("database", c.Database)
	add("file", c.FilePath)
	add("sslMode", c.SSLMode)
	add("schema", c.DefaultSchema)
}

func finish(c *model.ConnectionConfig) {
	if c.Driver == model.DriverPostgres && c.Database == "" {
		c.Database = "postgres"
	}
	switch {
	case c.Driver == model.DriverSQLite && c.FilePath != "":
		c.Name = filepath.Base(c.FilePath)
	case c.Database != "" && c.Host != "" && c.Host != "127.0.0.1" && c.Host != "localhost":
		c.Name = c.Database + " @ " + c.Host
	case c.Database != "":
		c.Name = c.Database
	case c.Host != "":
		c.Name = c.Host
	case c.Socket != "":
		c.Name = c.Driver + " (socket)"
	}
}

func unescape(s string) string {
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}

func driverFor(scheme string) string {
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql", "pgsql", "pg":
		return model.DriverPostgres
	case "mysql", "mariadb", "mysqlx":
		return model.DriverMySQL
	case "sqlite", "sqlite3", "file":
		return model.DriverSQLite
	}
	return ""
}

// parseURL handles scheme://user:pass@host:port/db?params, sqlite/file URLs and
// (jdbc=true) the JDBC variants. The authority is split on the LAST '@', so
// passwords with unescaped '@', '/', '#' or ':' still work.
func parseURL(c *model.ConnectionConfig, res *model.EnvImportResult, s string, jdbc bool) error {
	colon := strings.Index(s, ":")
	if colon <= 0 {
		return model.Errf(model.ErrInvalid, "%s", errUnknown)
	}
	scheme, rest := s[:colon], s[colon+1:]
	c.Driver = driverFor(scheme)
	if c.Driver == "" {
		return model.Errf(model.ErrInvalid, "unsupported scheme %q (mysql, mariadb, postgres, postgresql, sqlite, file)", scheme)
	}
	if c.Driver == model.DriverSQLite {
		return parseSQLiteURL(c, res, rest)
	}
	if !strings.HasPrefix(rest, "//") {
		return model.Errf(model.ErrInvalid, "expected %s://host/database", scheme)
	}
	rest = rest[2:]
	if i := strings.Index(rest, "#"); i >= 0 && !strings.Contains(rest[i:], "@") {
		rest = rest[:i] // fragment
	}
	var userinfo string
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo, rest = rest[:at], rest[at+1:]
	}
	hostpart, path, query := rest, "", ""
	if i := strings.IndexAny(hostpart, "/?"); i >= 0 {
		hostpart, path = hostpart[:i], hostpart[i:]
	}
	if i := strings.Index(path, "?"); i >= 0 {
		path, query = path[:i], path[i+1:]
	}
	if userinfo != "" {
		u, p, hasPw := strings.Cut(userinfo, ":")
		c.User = unescape(u)
		if hasPw {
			pw := unescape(p)
			c.Password = &pw
		}
	}
	if err := setHostPort(c, res, hostpart); err != nil {
		return err
	}
	if db := strings.TrimPrefix(path, "/"); db != "" {
		c.Database = unescape(db)
	}
	return applyQuery(c, res, query, jdbc)
}

func setHostPort(c *model.ConnectionConfig, res *model.EnvImportResult, hp string) error {
	if hp == "" {
		return nil
	}
	if strings.Contains(hp, ",") {
		res.Warnings = append(res.Warnings, "several hosts given; using the first one")
		hp = strings.Split(hp, ",")[0]
	}
	host, port := hp, ""
	switch {
	case strings.HasPrefix(hp, "["): // [::1]:5432
		end := strings.Index(hp, "]")
		if end < 0 {
			return model.Errf(model.ErrInvalid, "unterminated IPv6 address %q", hp)
		}
		host = hp[1:end]
		port = strings.TrimPrefix(hp[end+1:], ":")
	case strings.Count(hp, ":") == 1:
		host, port, _ = net.SplitHostPort(hp)
	case strings.Count(hp, ":") > 1: // bare IPv6 without port
		host = hp
	}
	c.Host = unescape(host)
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 {
			return model.Errf(model.ErrInvalid, "invalid port %q", port)
		}
		c.Port = p
	}
	return nil
}

func mapSSL(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "disable", "disabled", "false", "0", "off", "no":
		return model.SSLDisable, true
	case "allow", "prefer", "preferred":
		return model.SSLPrefer, true
	case "require", "required", "true", "1", "on", "yes", "skip-verify":
		return model.SSLRequire, true
	case "verify-ca", "verify_ca":
		return model.SSLVerifyCA, true
	case "verify-full", "verify_full", "verify_identity", "verify-identity":
		return model.SSLVerifyFull, true
	}
	return "", false
}

// applyQuery maps query parameters onto the config.
func applyQuery(c *model.ConnectionConfig, res *model.EnvImportResult, query string, jdbc bool) error {
	if query == "" {
		return nil
	}
	q, err := url.ParseQuery(strings.ReplaceAll(query, ";", "&")) // JDBC also allows ';'
	if err != nil {
		res.Warnings = append(res.Warnings, "some parameters could not be read")
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ignored []string
	for _, k := range keys {
		v := q.Get(k)
		switch lk := strings.ToLower(k); lk {
		case "sslmode", "ssl-mode", "ssl_mode", "tls", "ssl", "usessl":
			if m, ok := mapSSL(v); ok {
				c.SSLMode = m
			} else {
				res.Warnings = append(res.Warnings, "unknown "+k+" value "+strconv.Quote(v)+"; SSL mode left at default")
			}
		case "socket", "unix_socket", "unixsocket":
			c.Socket = v
		case "host":
			if strings.HasPrefix(v, "/") { // postgres socket directory
				c.Socket = v
			} else if c.Host == "" {
				c.Host = v
			}
		case "port":
			if p, err := strconv.Atoi(v); err == nil && c.Port == 0 {
				c.Port = p
			}
		case "dbname", "database":
			if c.Database == "" {
				c.Database = v
			}
		case "user", "username":
			if c.User == "" {
				c.User = v
			}
		case "password":
			if c.Password == nil {
				pw := v
				c.Password = &pw
			}
		case "schema", "search_path", "currentschema":
			c.DefaultSchema = strings.TrimSpace(strings.Split(v, ",")[0])
		case "charset", "characterencoding":
			if lk == "charset" {
				res.Warnings = append(res.Warnings, "charset="+v+" ignored (utf8mb4 is always used)")
			}
		default:
			if !silentQuery[lk] {
				ignored = append(ignored, k)
			}
		}
	}
	if len(ignored) > 0 {
		res.Warnings = append(res.Warnings, "ignored parameters: "+strings.Join(ignored, ", "))
	}
	return nil
}

func parseSQLiteURL(c *model.ConnectionConfig, res *model.EnvImportResult, rest string) error {
	path, query, _ := strings.Cut(rest, "?")
	switch {
	case strings.HasPrefix(path, "///"): // sqlite:///abs/path
		path = path[2:]
	case strings.HasPrefix(path, "//"): // sqlite://relative/path or file://localhost/abs
		path = strings.TrimPrefix(path[2:], "localhost")
	}
	path = unescape(path)
	if path == "" || path == ":memory:" {
		return model.Errf(model.ErrInvalid, "the SQLite URL has no file path")
	}
	if strings.HasPrefix(path, "~/") {
		// kept: the driver expands ~/
	} else if !filepath.IsAbs(path) {
		res.Warnings = append(res.Warnings, "relative SQLite path "+strconv.Quote(path)+"; pick the file to make it absolute")
	}
	c.FilePath = path
	if query != "" {
		q, _ := url.ParseQuery(query)
		if q.Get("mode") == "ro" || q.Get("immutable") == "1" {
			c.ReadOnly = true
			res.Warnings = append(res.Warnings, "mode=ro: read-only enabled")
		}
	}
	return nil
}

// parseMySQLDSN handles user:pass@tcp(host:port)/db?params and user@unix(/sock)/db.
func parseMySQLDSN(c *model.ConnectionConfig, res *model.EnvImportResult, s string) error {
	// split on the last '@' before the protocol so passwords may contain '@'
	proto := -1
	for _, p := range []string{"@tcp(", "@tcp6(", "@unix("} {
		if i := strings.LastIndex(s, p); i > proto {
			proto = i
		}
	}
	userinfo, rest := "", s
	if proto >= 0 {
		userinfo, rest = s[:proto], s[proto+1:]
	}
	m := mysqlDSNRe.FindStringSubmatch(rest)
	if m == nil {
		return model.Errf(model.ErrInvalid, "%s", errUnknown)
	}
	c.Driver = model.DriverMySQL
	if userinfo != "" {
		u, p, hasPw := strings.Cut(userinfo, ":")
		c.User = u
		if hasPw {
			pw := p
			c.Password = &pw
		}
	}
	switch m[2] {
	case "unix":
		c.Socket = m[3]
	default:
		if err := setHostPort(c, res, m[3]); err != nil {
			return err
		}
	}
	c.Database = unescape(m[4])
	return applyQuery(c, res, m[5], false)
}

// parseLibpq handles host=... port=... dbname=... user=... password='a b' sslmode=...
func parseLibpq(c *model.ConnectionConfig, res *model.EnvImportResult, s string) error {
	kv := map[string]string{}
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return model.Errf(model.ErrInvalid, "expected key=value at %q", strings.Fields(s[i:])[0])
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		for i < len(s) && s[i] == ' ' {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '\'' {
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '\'' {
					i++
					closed = true
					break
				}
				val.WriteByte(s[i])
				i++
			}
			if !closed {
				return model.Errf(model.ErrInvalid, "unterminated quoted value for %s", key)
			}
		} else {
			for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
		}
		kv[strings.ToLower(key)] = val.String()
	}
	known := 0
	for k := range kv {
		if libpqKnown[k] {
			known++
		}
	}
	if known == 0 {
		return model.Errf(model.ErrInvalid, "%s", errUnknown)
	}
	c.Driver = model.DriverPostgres
	if h := kv["host"]; h != "" {
		if strings.HasPrefix(h, "/") {
			c.Socket = h
		} else if err := setHostPort(c, res, strings.Split(h, ",")[0]); err != nil {
			return err
		}
		if strings.Contains(h, ",") {
			res.Warnings = append(res.Warnings, "several hosts given; using the first one")
		}
	} else if h := kv["hostaddr"]; h != "" {
		c.Host = h
	}
	if p := kv["port"]; p != "" {
		n, err := strconv.Atoi(strings.Split(p, ",")[0])
		if err != nil {
			return model.Errf(model.ErrInvalid, "invalid port %q", p)
		}
		c.Port = n
	}
	c.Database = kv["dbname"]
	c.User = kv["user"]
	if pw, ok := kv["password"]; ok {
		c.Password = &pw
	}
	if v, ok := kv["sslmode"]; ok {
		if m, ok := mapSSL(v); ok {
			c.SSLMode = m
		} else {
			res.Warnings = append(res.Warnings, "unknown sslmode "+strconv.Quote(v))
		}
	}
	if o := kv["options"]; strings.Contains(o, "search_path=") {
		v := o[strings.Index(o, "search_path=")+len("search_path="):]
		c.DefaultSchema = strings.Split(strings.Fields(v)[0], ",")[0]
	}
	var ignored []string
	for k := range kv {
		switch k {
		case "host", "hostaddr", "port", "dbname", "user", "password", "sslmode", "options", "application_name", "connect_timeout":
		default:
			ignored = append(ignored, k)
		}
	}
	sort.Strings(ignored)
	if len(ignored) > 0 {
		res.Warnings = append(res.Warnings, "ignored parameters: "+strings.Join(ignored, ", "))
	}
	return nil
}
