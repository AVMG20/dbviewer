// Package store persists app state (projects, connections, history, saved queries,
// UI state) in a SQLite file and secrets in secrets.json (0600) in the same data
// directory (0700). The macOS Keychain is not used.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dbviewer/internal/model"

	_ "modernc.org/sqlite"
)

// DataDir returns the app-state directory: $DBVIEWER_DATA_DIR or
// ~/Library/Application Support/dbviewer (os.UserConfigDir()/dbviewer).
func DataDir() (string, error) {
	if d := os.Getenv("DBVIEWER_DATA_DIR"); d != "" {
		return filepath.Abs(d)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dbviewer"), nil
}

type Store struct {
	db      *sql.DB
	dir     string
	secrets Secrets
	mu      sync.Mutex
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  config TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS query_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  conn_id TEXT NOT NULL,
  sql TEXT NOT NULL,
  ran_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  row_count INTEGER NOT NULL DEFAULT -1,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_history_conn ON query_history(conn_id, id);
CREATE TABLE IF NOT EXISTS saved_queries (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  conn_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  sql TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ui_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Open opens (and creates) the store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // secrets live here: owner only
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("init state db: %w", err)
	}
	s := &Store{db: db, dir: dir, secrets: newSecrets(dir)}
	if err := s.ensureDefaultProject(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Dir() string  { return s.dir }

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) ensureDefaultProject() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO projects (id, name, sort_order) VALUES (?, ?, 0)`, NewID(), "Default")
	return err
}

// ---------------------------------------------------------------------------
// Projects

func (s *Store) Projects() ([]model.Project, error) {
	rows, err := s.db.Query(`SELECT id, name, sort_order FROM projects ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Project{}
	for rows.Next() {
		var p model.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SaveProject(p model.Project) (model.Project, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, model.Errf(model.ErrInvalid, "project name is required")
	}
	if p.ID == "" {
		p.ID = NewID()
		if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM projects`).Scan(&p.SortOrder); err != nil {
			return p, err
		}
		_, err := s.db.Exec(`INSERT INTO projects (id, name, sort_order) VALUES (?, ?, ?)`, p.ID, p.Name, p.SortOrder)
		return p, err
	}
	res, err := s.db.Exec(`UPDATE projects SET name = ?, sort_order = ? WHERE id = ?`, p.Name, p.SortOrder, p.ID)
	if err != nil {
		return p, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, model.Errf(model.ErrNotFound, "project %s not found", p.ID)
	}
	return p, nil
}

// DeleteProject deletes a project with its connections and saved queries.
// The last remaining project cannot be deleted.
func (s *Store) DeleteProject(id string) ([]string, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		return nil, err
	}
	if n <= 1 {
		return nil, model.Errf(model.ErrInvalid, "cannot delete the last project")
	}
	conns, err := s.Connections(id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range conns {
		ids = append(ids, c.ID)
		s.deleteSecrets(c.ID)
	}
	if _, err := s.db.Exec(`DELETE FROM saved_queries WHERE project_id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) ReorderProjects(ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE projects SET sort_order = ? WHERE id = ?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Connections

// Connections lists connections of a project ("" = all), without secrets.
func (s *Store) Connections(projectID string) ([]model.ConnectionConfig, error) {
	q := `SELECT config, sort_order, created_at, updated_at FROM connections`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY sort_order, created_at`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionConfig{}
	for rows.Next() {
		var raw string
		var c model.ConnectionConfig
		var so int
		var ca, ua string
		if err := rows.Scan(&raw, &so, &ca, &ua); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		c.SortOrder, c.CreatedAt, c.UpdatedAt = so, ca, ua
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if secrets, err := s.secrets.All(); err == nil {
		for i := range out {
			if s.syncSecretFlags(&out[i], secrets) {
				s.storeFlags(out[i])
			}
		}
	}
	return out, nil
}

// syncSecretFlags makes hasPassword / ssh.hasPassword / ssh.hasKeyPassphrase
// reflect whether the secret really exists in the secrets file (flags saved by an
// older build may point at the Keychain). Reports whether anything changed.
func (s *Store) syncSecretFlags(c *model.ConnectionConfig, secrets map[string]string) bool {
	has := func(kind string) bool { _, ok := secrets[secretKey(c.ID, kind)]; return ok }
	pw, sp, kp := has(secretPassword), has(secretSSHPassword), has(secretSSHPassphrase)
	changed := c.HasPassword != pw || c.SSH.HasPassword != sp || c.SSH.HasKeyPassphrase != kp
	c.HasPassword, c.SSH.HasPassword, c.SSH.HasKeyPassphrase = pw, sp, kp
	return changed
}

// storeFlags persists the (fixed-up) secret flags of a connection.
func (s *Store) storeFlags(c model.ConnectionConfig) {
	c.Password, c.SSH.Password, c.SSH.KeyPassphrase = nil, nil, nil
	if raw, err := json.Marshal(c); err == nil {
		_, _ = s.db.Exec(`UPDATE connections SET config = ? WHERE id = ?`, string(raw), c.ID)
	}
}

// SaveSecret stores a secret of a saved connection (kind: password |
// ssh-password | ssh-passphrase) and sets its flag. Used to persist a password
// entered at connect time once the connect succeeded.
func (s *Store) SaveSecret(connID, kind, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.secrets.Set(secretKey(connID, kind), value); err != nil {
		return model.Wrap(model.ErrInternal, err)
	}
	c, err := s.Connection(connID)
	if err != nil {
		return err
	}
	s.storeFlags(c)
	return nil
}

func (s *Store) Connection(id string) (model.ConnectionConfig, error) {
	var raw string
	var c model.ConnectionConfig
	var so int
	var ca, ua string
	err := s.db.QueryRow(`SELECT config, sort_order, created_at, updated_at FROM connections WHERE id = ?`, id).Scan(&raw, &so, &ca, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return c, model.Errf(model.ErrNotFound, "connection %s not found", id)
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	c.SortOrder, c.CreatedAt, c.UpdatedAt = so, ca, ua
	if secrets, err := s.secrets.All(); err == nil && s.syncSecretFlags(&c, secrets) {
		s.storeFlags(c)
	}
	return c, nil
}

// Normalize fills defaults and validates a config (no secrets touched).
func Normalize(c *model.ConnectionConfig) error {
	c.Name = strings.TrimSpace(c.Name)
	switch c.Driver {
	case model.DriverPostgres, model.DriverMySQL, model.DriverSQLite:
	case "pgsql", "postgresql":
		c.Driver = model.DriverPostgres
	case "mariadb":
		c.Driver = model.DriverMySQL
	default:
		return model.Errf(model.ErrInvalid, "unknown driver %q", c.Driver)
	}
	if c.Driver == model.DriverSQLite && strings.TrimSpace(c.FilePath) == "" {
		return model.Errf(model.ErrInvalid, "SQLite file path is required")
	}
	if c.Name == "" {
		switch {
		case c.Driver == model.DriverSQLite:
			c.Name = filepath.Base(c.FilePath)
		case c.Database != "":
			c.Name = c.Database
		default:
			c.Name = c.Driver
		}
	}
	if c.Color == "" {
		c.Color = model.ColorNone
	}
	switch c.Color {
	case model.ColorNone, model.ColorGreen, model.ColorYellow, model.ColorOrange, model.ColorRed, model.ColorBlue, model.ColorPurple:
	default:
		return model.Errf(model.ErrInvalid, "unknown color %q", c.Color)
	}
	if c.PasswordMode == "" {
		c.PasswordMode = model.PasswordSave
	}
	if c.PasswordMode != model.PasswordSave && c.PasswordMode != model.PasswordAsk {
		return model.Errf(model.ErrInvalid, "unknown password mode %q", c.PasswordMode)
	}
	switch c.SSLMode {
	case "", model.SSLDisable, model.SSLPrefer, model.SSLRequire, model.SSLVerifyCA, model.SSLVerifyFull:
	default:
		return model.Errf(model.ErrInvalid, "unknown SSL mode %q (disable, prefer, require, verify-ca, verify-full)", c.SSLMode)
	}
	switch c.SSH.AuthMethod {
	case "", "agent":
		c.SSH.AuthMethod = "auto"
	case "auto", "key", "password":
	default:
		return model.Errf(model.ErrInvalid, "unknown SSH auth method %q (auto, key, password)", c.SSH.AuthMethod)
	}
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	return nil
}

// SaveConnection creates (ID == "") or updates a connection. Write-only secrets in
// cfg (Password, SSH.Password, SSH.KeyPassphrase) are applied to the secrets file:
// nil = keep, "" = delete, other = store.
func (s *Store) SaveConnection(c model.ConnectionConfig) (model.ConnectionConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := Normalize(&c); err != nil {
		return c, err
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, c.ProjectID).Scan(&exists); err != nil {
		return c, err
	}
	if exists == 0 {
		return c, model.Errf(model.ErrInvalid, "project %q does not exist", c.ProjectID)
	}
	isNew := c.ID == ""
	if isNew {
		c.ID = NewID()
		c.CreatedAt = now()
		if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM connections WHERE project_id = ?`, c.ProjectID).Scan(&c.SortOrder); err != nil {
			return c, err
		}
	} else {
		old, err := s.Connection(c.ID)
		if err != nil {
			return c, err
		}
		c.CreatedAt = old.CreatedAt
		c.HasPassword, c.SSH.HasPassword, c.SSH.HasKeyPassphrase = old.HasPassword, old.SSH.HasPassword, old.SSH.HasKeyPassphrase
	}
	c.UpdatedAt = now()

	if err := s.applySecret(c.ID, secretPassword, c.Password, &c.HasPassword); err != nil {
		return c, err
	}
	if err := s.applySecret(c.ID, secretSSHPassword, c.SSH.Password, &c.SSH.HasPassword); err != nil {
		return c, err
	}
	if err := s.applySecret(c.ID, secretSSHPassphrase, c.SSH.KeyPassphrase, &c.SSH.HasKeyPassphrase); err != nil {
		return c, err
	}
	if c.PasswordMode == model.PasswordAsk && c.HasPassword {
		_ = s.secrets.Delete(secretKey(c.ID, secretPassword))
		c.HasPassword = false
	}
	c.Password, c.SSH.Password, c.SSH.KeyPassphrase = nil, nil, nil

	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if isNew {
		_, err = s.db.Exec(`INSERT INTO connections (id, project_id, config, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			c.ID, c.ProjectID, string(raw), c.SortOrder, c.CreatedAt, c.UpdatedAt)
	} else {
		_, err = s.db.Exec(`UPDATE connections SET project_id = ?, config = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
			c.ProjectID, string(raw), c.SortOrder, c.UpdatedAt, c.ID)
	}
	return c, err
}

func (s *Store) applySecret(connID, kind string, v *string, has *bool) error {
	if v == nil {
		return nil
	}
	key := secretKey(connID, kind)
	if *v == "" {
		if err := s.secrets.Delete(key); err != nil {
			return model.Wrap(model.ErrInternal, err)
		}
		*has = false
		return nil
	}
	if err := s.secrets.Set(key, *v); err != nil {
		return model.Wrap(model.ErrInternal, err)
	}
	*has = true
	return nil
}

func (s *Store) DeleteConnection(id string) error {
	if _, err := s.db.Exec(`DELETE FROM connections WHERE id = ?`, id); err != nil {
		return err
	}
	s.deleteSecrets(id)
	_, err := s.db.Exec(`DELETE FROM query_history WHERE conn_id = ?`, id)
	return err
}

func (s *Store) ReorderConnections(projectID string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE connections SET sort_order = ?, project_id = ? WHERE id = ?`, i, projectID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SSHKeyPassphrase returns the saved passphrase of an SSH key file ("" if none).
func (s *Store) SSHKeyPassphrase(path string) (string, error) {
	v, _, err := s.secrets.Get("sshkey:" + path)
	return v, err
}

// SetSSHKeyPassphrase saves ("" = deletes) the passphrase of an SSH key file in the secrets file.
func (s *Store) SetSSHKeyPassphrase(path, pass string) error {
	if pass == "" {
		return s.secrets.Delete("sshkey:" + path)
	}
	return s.secrets.Set("sshkey:"+path, pass)
}

// Secret returns a stored secret ("" if none). kind: password | ssh-password | ssh-passphrase.
func (s *Store) Secret(connID, kind string) (string, error) {
	v, _, err := s.SecretOK(connID, kind)
	return v, err
}

// SecretOK is Secret that also reports whether the secret exists in the file
// (it does not for passwords that used to be in the Keychain).
func (s *Store) SecretOK(connID, kind string) (string, bool, error) {
	v, ok, err := s.secrets.Get(secretKey(connID, kind))
	if err != nil {
		return "", false, model.Wrap(model.ErrInternal, err)
	}
	return v, ok, nil
}

func (s *Store) deleteSecrets(connID string) {
	for _, k := range []string{secretPassword, secretSSHPassword, secretSSHPassphrase} {
		_ = s.secrets.Delete(secretKey(connID, k))
	}
}

// ---------------------------------------------------------------------------
// History

// HistoryLimit is the number of history entries kept per connection.
const HistoryLimit = 5000

var secretClauseRe = regexp.MustCompile(`(?i)\b(IDENTIFIED\s+(?:WITH\s+\S+\s+)?(?:BY|AS)|PASSWORD\s*(?:=|\()?|ENCRYPTED\s+PASSWORD|SECRET)\s*('(?:[^']|'')*'|"(?:[^"]|"")*")`)

// MaskSecrets replaces password literals in CREATE/ALTER USER/ROLE-style
// statements with '***' before they are stored in the history.
func MaskSecrets(sql string) string {
	return secretClauseRe.ReplaceAllStringFunc(sql, func(m string) string {
		sub := secretClauseRe.FindStringSubmatch(m)
		return sub[1] + " '***'"
	})
}

func (s *Store) AddHistory(h model.QueryHistory) error {
	if h.RanAt == "" {
		h.RanAt = now()
	}
	h.SQL = MaskSecrets(h.SQL)
	res, err := s.db.Exec(`INSERT INTO query_history (conn_id, sql, ran_at, duration_ms, row_count, error) VALUES (?, ?, ?, ?, ?, ?)`,
		h.ConnID, h.SQL, h.RanAt, h.DurationMs, h.RowCount, h.Error)
	if err != nil {
		return err
	}
	_ = res
	// prune in batches: keep the newest HistoryLimit entries per connection
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM query_history WHERE conn_id = ?`, h.ConnID).Scan(&n); err == nil && n > HistoryLimit+100 {
		_, err = s.db.Exec(`DELETE FROM query_history WHERE conn_id = ? AND id <= (
  SELECT id FROM query_history WHERE conn_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`, h.ConnID, h.ConnID, HistoryLimit)
		return err
	}
	return nil
}

// History returns newest first. search filters on SQL text (case-insensitive substring).
func (s *Store) History(connID, search string, limit int) ([]model.QueryHistory, error) {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	q := `SELECT id, conn_id, sql, ran_at, duration_ms, row_count, error FROM query_history WHERE 1=1`
	var args []any
	if connID != "" {
		q += ` AND conn_id = ?`
		args = append(args, connID)
	}
	if search != "" {
		q += ` AND sql LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(search)+"%")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.QueryHistory{}
	for rows.Next() {
		var h model.QueryHistory
		if err := rows.Scan(&h.ID, &h.ConnID, &h.SQL, &h.RanAt, &h.DurationMs, &h.RowCount, &h.Error); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) ClearHistory(connID string) error {
	if connID == "" {
		_, err := s.db.Exec(`DELETE FROM query_history`)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM query_history WHERE conn_id = ?`, connID)
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---------------------------------------------------------------------------
// Saved queries

func (s *Store) SavedQueries(projectID string) ([]model.SavedQuery, error) {
	q := `SELECT id, project_id, conn_id, name, sql, created_at, updated_at FROM saved_queries`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY name COLLATE NOCASE`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SavedQuery{}
	for rows.Next() {
		var sq model.SavedQuery
		if err := rows.Scan(&sq.ID, &sq.ProjectID, &sq.ConnID, &sq.Name, &sq.SQL, &sq.CreatedAt, &sq.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sq)
	}
	return out, rows.Err()
}

func (s *Store) SaveQuery(q model.SavedQuery) (model.SavedQuery, error) {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		return q, model.Errf(model.ErrInvalid, "name is required")
	}
	if q.ProjectID == "" {
		return q, model.Errf(model.ErrInvalid, "projectId is required")
	}
	q.UpdatedAt = now()
	if q.ID == "" {
		q.ID = NewID()
		q.CreatedAt = q.UpdatedAt
		_, err := s.db.Exec(`INSERT INTO saved_queries (id, project_id, conn_id, name, sql, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			q.ID, q.ProjectID, q.ConnID, q.Name, q.SQL, q.CreatedAt, q.UpdatedAt)
		return q, err
	}
	res, err := s.db.Exec(`UPDATE saved_queries SET project_id = ?, conn_id = ?, name = ?, sql = ?, updated_at = ? WHERE id = ?`,
		q.ProjectID, q.ConnID, q.Name, q.SQL, q.UpdatedAt, q.ID)
	if err != nil {
		return q, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return q, model.Errf(model.ErrNotFound, "saved query %s not found", q.ID)
	}
	_ = s.db.QueryRow(`SELECT created_at FROM saved_queries WHERE id = ?`, q.ID).Scan(&q.CreatedAt)
	return q, nil
}

func (s *Store) DeleteSavedQuery(id string) error {
	_, err := s.db.Exec(`DELETE FROM saved_queries WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------------------
// UI state (opaque JSON strings owned by the frontend)

func (s *Store) UIState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM ui_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetUIState(key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM ui_state WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO ui_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
