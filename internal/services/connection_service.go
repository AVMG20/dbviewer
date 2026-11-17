package services

import (
	"context"
	"strings"

	"dbviewer/internal/envimport"
	"dbviewer/internal/model"
	"dbviewer/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ConnectionService: connection CRUD, test/connect, .env import, file dialogs.
type ConnectionService struct{ c *Core }

func NewConnectionService(c *Core) *ConnectionService { return &ConnectionService{c: c} }

// Connections lists connections of a project ("" = all) ordered by sortOrder.
// Secrets are never included; see hasPassword.
func (s *ConnectionService) Connections(projectID string) ([]model.ConnectionConfig, error) {
	c, err := s.c.Store.Connections(projectID)
	return ret(c, err)
}

func (s *ConnectionService) GetConnection(id string) (model.ConnectionConfig, error) {
	c, err := s.c.Store.Connection(id)
	return ret(c, err)
}

// SaveConnection creates (id "") or updates a connection and returns the stored
// version. password: omit/null = keep, "" = clear, value = save in the secrets file.
// An open pool for this connection is closed (it reconnects on next use).
func (s *ConnectionService) SaveConnection(cfg model.ConnectionConfig) (model.ConnectionConfig, error) {
	out, err := s.c.Store.SaveConnection(cfg)
	if err != nil {
		return ret(out, err)
	}
	if cfg.ID != "" {
		s.c.Conns.Reconfigured(cfg.ID)
		s.c.Meta.Invalidate(cfg.ID, "")
	}
	return out, nil
}

// DeleteConnection disconnects and deletes a connection, its secrets and history.
func (s *ConnectionService) DeleteConnection(id string) error {
	s.c.Conns.Disconnect(id)
	s.c.Meta.Invalidate(id, "")
	return wrap(s.c.Store.DeleteConnection(id))
}

// DuplicateConnection copies a connection (name + " copy"), including its stored secrets.
func (s *ConnectionService) DuplicateConnection(id string) (model.ConnectionConfig, error) {
	src, err := s.c.Store.Connection(id)
	if err != nil {
		return ret(src, err)
	}
	cp := src
	cp.ID, cp.Name = "", src.Name+" copy"
	if src.HasPassword {
		pw, err := s.c.Store.Secret(id, store.SecretPassword)
		if err != nil {
			return ret(cp, err)
		}
		cp.Password = &pw
	}
	if src.SSH.HasPassword {
		pw, _ := s.c.Store.Secret(id, store.SecretSSHPassword)
		cp.SSH.Password = &pw
	}
	if src.SSH.HasKeyPassphrase {
		pw, _ := s.c.Store.Secret(id, store.SecretSSHPassphrase)
		cp.SSH.KeyPassphrase = &pw
	}
	out, err := s.c.Store.SaveConnection(cp)
	return ret(out, err)
}

// ReorderConnections sets sortOrder (and projectId, allowing moves between projects).
func (s *ConnectionService) ReorderConnections(projectID string, ids []string) error {
	return wrap(s.c.Store.ReorderConnections(projectID, ids))
}

// TestConnection opens and closes a temporary connection using cfg as given
// (unsaved form state). Uses cfg.password when set, otherwise the stored secret.
func (s *ConnectionService) TestConnection(ctx context.Context, cfg model.ConnectionConfig) (model.TestResult, error) {
	r, err := s.c.Conns.Test(ctx, cfg)
	return ret(r, err)
}

// Connect opens the pool. password is only used for passwordMode "ask".
// Emits "conn:status". Connections in "save" mode also connect lazily on first use.
func (s *ConnectionService) Connect(ctx context.Context, id string, password string) (model.ConnectionStatus, error) {
	st, err := s.c.Conns.Connect(ctx, id, password)
	return ret(st, err)
}

// Disconnect closes the pool and drops cached metadata. Emits "conn:status".
func (s *ConnectionService) Disconnect(id string) error {
	s.c.Conns.Disconnect(id)
	s.c.Meta.Invalidate(id, "")
	return nil
}

// ConnectionStatuses returns the status of every saved connection.
func (s *ConnectionService) ConnectionStatuses() ([]model.ConnectionStatus, error) {
	st, err := s.c.Conns.Statuses()
	return ret(st, err)
}

// UseDatabase switches the database of an open connection (postgres: reconnects to
// that database; mysql: changes the default schema). Not persisted.
// Console sessions of the connection are closed; metadata caches are dropped.
func (s *ConnectionService) UseDatabase(ctx context.Context, id string, database string) (model.ConnectionStatus, error) {
	if database == "" {
		return model.ConnectionStatus{ConnID: id}, model.Errf(model.ErrInvalid, "database is required")
	}
	st, err := s.c.Conns.UseDatabase(ctx, id, database)
	return ret(st, err)
}

// TrustHostKey accepts an SSH host key reported by a host_key_unknown error /
// "ssh:hostkey" event (accept on first use): it is added to the app's
// known_hosts file. Retry Connect/TestConnection afterwards.
func (s *ConnectionService) TrustHostKey(host string, fingerprint string) error {
	return wrap(s.c.Conns.TrustHostKey(host, fingerprint))
}

// ParseEnv parses pasted .env text into a prefilled config (not saved).
func (s *ConnectionService) ParseEnv(text string) (model.EnvImportResult, error) {
	return envimport.Parse(text, ""), nil
}

// TestSSH tests only the SSH tunnel of an (unsaved) config: handshake and login
// with the same auth chain as Connect (host_key_unknown / host_key_mismatch /
// passphrase_required / ssh_error flows included), then a direct channel to the
// database host:port (or socket) from the SSH host, closed at once (no database
// login). Login failures reject; an unreachable database endpoint resolves with
// dbReachable=false and dbError.
func (s *ConnectionService) TestSSH(ctx context.Context, cfg model.ConnectionConfig) (model.SSHTestResult, error) {
	r, err := s.c.Conns.TestSSH(ctx, cfg)
	return ret(r, err)
}

// ProvideKeyPassphrase supplies the passphrase of an encrypted SSH key file
// (after a passphrase_required error / "ssh:passphrase" event). It is checked
// against the file and kept for this app session; remember=true also stores it
// in the secrets file. Retry Connect/TestConnection/TestSSH afterwards.
// Rejects with invalid_argument on a wrong passphrase.
func (s *ConnectionService) ProvideKeyPassphrase(keyPath string, passphrase string, remember bool) error {
	return wrap(s.c.Conns.ProvideKeyPassphrase(keyPath, passphrase, remember))
}

// ParseConnectionString parses a pasted connection string into a prefilled config
// (not saved): database URLs (mysql://, mariadb://, postgres://, postgresql://,
// pgsql://, sqlite://, sqlite:///abs, file:), JDBC URLs, Go MySQL DSNs
// (user:pass@tcp(host:3306)/db, user@unix(/sock)/db), libpq key=value strings and
// .env lines/blobs (same as ParseEnv). found["format"] is url | jdbc | mysql-dsn |
// libpq | env; the other found keys summarize what was recognized (password
// masked). Unrecognized input rejects with invalid_argument.
func (s *ConnectionService) ParseConnectionString(text string) (model.EnvImportResult, error) {
	r, err := envimport.ParseConnectionString(text)
	return ret(r, err)
}

// ImportEnvFile parses an .env file. path "" opens a native file dialog;
// a cancelled dialog returns {cancelled: true} without error.
func (s *ConnectionService) ImportEnvFile(path string) (model.EnvImportResult, error) {
	if path == "" {
		p, err := openFileDialog("Import .env file", "")
		if err != nil {
			return model.EnvImportResult{}, wrap(err)
		}
		if p == "" {
			return model.EnvImportResult{Cancelled: true}, nil
		}
		path = p
	}
	r, err := envimport.ParseFile(path)
	return ret(r, err)
}

// OpenSQLiteFileDialog shows a native open dialog for a SQLite file; "" if cancelled.
func (s *ConnectionService) OpenSQLiteFileDialog() (string, error) {
	p, err := openFileDialog("Open SQLite database", "*.sqlite;*.sqlite3;*.db;*.db3")
	return ret(p, err)
}

func openFileDialog(title, pattern string) (string, error) {
	if serverMode {
		return "", model.Errf(model.ErrUnsupported, "native file dialogs are not available in browser mode; enter the path instead")
	}
	app := application.Get()
	if app == nil {
		return "", model.Errf(model.ErrInternal, "no application")
	}
	d := app.Dialog.OpenFile().SetTitle(title).CanChooseFiles(true).ShowHiddenFiles(true)
	if pattern != "" {
		d = d.AddFilter("SQLite database", pattern).AddFilter("All files", "*")
	}
	p, err := d.PromptForSingleSelection()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "cancel") {
		return "", nil
	}
	return p, err
}
