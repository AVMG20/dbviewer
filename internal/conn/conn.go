// Package conn manages open connection pools, their status, SSH tunnels and
// read-only enforcement.
package conn

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"dbviewer/internal/driver"
	"dbviewer/internal/model"
	"dbviewer/internal/store"

	"golang.org/x/crypto/ssh"
)

// Pool is an open connection.
type Pool struct {
	Config        model.ConnectionConfig
	Driver        driver.Driver
	DB            *sql.DB
	ServerVersion string
	Database      string
	DefaultSchema string
	password      string
	closers       []func()
}

// Close closes the pool and its SSH tunnel.
func (p *Pool) Close() {
	p.DB.Close()
	for _, c := range p.closers {
		c()
	}
}

// ReadOnly reports whether writes must be refused.
func (p *Pool) ReadOnly() bool { return p.Config.ReadOnly }

// Schema resolves "" to the default schema.
func (p *Pool) Schema(s string) string {
	if s == "" {
		return p.DefaultSchema
	}
	return s
}

type Manager struct {
	store     *store.Store
	mu        sync.Mutex
	pools     map[string]*Pool
	errors    map[string]string
	pending   pendingKeys
	connMu    sync.Map                     // connID -> *sync.Mutex: serializes lazy connects
	OnStatus  func(model.ConnectionStatus) // optional; called on every status change
	OnHostKey func(model.HostKeyPrompt)    // optional; unknown SSH host key seen
	// OnKeyPassphrase: the server accepted an encrypted key file whose passphrase
	// is unknown (optional).
	OnKeyPassphrase func(model.SSHKeyPrompt)
	keyPass         keyPassphrases
	OnClose         func(connID string) // optional; a pool was closed/replaced
}

func NewManager(s *store.Store) *Manager {
	return &Manager{store: s, pools: map[string]*Pool{}, errors: map[string]string{}, pending: pendingKeys{keys: map[string]ssh.PublicKey{}}, keyPass: keyPassphrases{m: map[string]string{}}}
}

func (m *Manager) emit(st model.ConnectionStatus) {
	if m.OnStatus != nil {
		m.OnStatus(st)
	}
}

// resolvePassword returns the password to use: explicit (cfg.Password or the
// argument) wins over the saved one.
func (m *Manager) resolvePassword(cfg model.ConnectionConfig, explicit *string) (string, error) {
	if explicit != nil {
		return *explicit, nil
	}
	if cfg.Password != nil {
		return *cfg.Password, nil
	}
	if cfg.ID != "" && cfg.HasPassword {
		v, ok, err := m.store.SecretOK(cfg.ID, store.SecretPassword)
		if err != nil {
			return "", err
		}
		if !ok {
			// saved before secrets moved out of the Keychain (never read, to avoid
			// prompts): behave as "no saved password"
			return "", model.Errf(model.ErrPasswordNeeded, "the password of %q is not saved; enter it (it can be saved again in the connection settings)", cfg.Name)
		}
		return v, nil
	}
	return "", nil
}

func (m *Manager) open(ctx context.Context, cfg model.ConnectionConfig, password string) (*Pool, error) {
	return m.openWith(ctx, cfg, password, false)
}

// openWith opens a pool; track=true reports a lost SSH tunnel for a managed pool.
func (m *Manager) openWith(ctx context.Context, cfg model.ConnectionConfig, password string, track bool) (*Pool, error) {
	d, err := driver.Get(cfg.Driver)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var closers []func()
	opts := driver.OpenOptions{Config: cfg, Password: password, ReadOnly: cfg.ReadOnly,
		AddCloser: func(f func()) { closers = append(closers, f) }}
	var pool atomic.Pointer[Pool]
	var closing atomic.Bool
	if cfg.SSH.Enabled && cfg.Driver != model.DriverSQLite {
		var onLost func()
		if track {
			onLost = func() {
				if p := pool.Load(); !closing.Load() && p != nil {
					m.tunnelLost(cfg.ID, p)
				}
			}
		}
		client, err := m.dialSSH(ctx, cfg, onLost)
		if err != nil {
			return nil, err
		}
		opts.Dial = sshDialer(client)
		closers = append(closers, func() { closing.Store(true); client.Close() })
	}
	db, err := d.Open(ctx, opts)
	if err != nil {
		for _, c := range closers {
			c()
		}
		var ae *model.AppError
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, model.Wrap(model.ErrDB, err)
	}
	p := &Pool{Config: cfg, Driver: d, DB: db, password: password, closers: closers}
	pool.Store(p)
	p.ServerVersion, _ = d.ServerVersion(ctx, db)
	p.Database, _ = d.CurrentDatabase(ctx, db)
	p.DefaultSchema, _ = d.DefaultSchema(ctx, db)
	return p, nil
}

// Test opens a temporary connection with cfg (cfg.Password if set, else the stored one).
func (m *Manager) Test(ctx context.Context, cfg model.ConnectionConfig) (model.TestResult, error) {
	if err := store.Normalize(&cfg); err != nil {
		return model.TestResult{}, err
	}
	if cfg.ID != "" && cfg.Password == nil {
		if old, err := m.store.Connection(cfg.ID); err == nil {
			cfg.HasPassword = old.HasPassword
		}
	}
	pw, err := m.resolvePassword(cfg, nil)
	if err != nil {
		return model.TestResult{}, err
	}
	start := time.Now()
	p, err := m.open(ctx, cfg, pw)
	if err != nil {
		return model.TestResult{}, err
	}
	defer p.Close()
	return model.TestResult{ServerVersion: p.ServerVersion, LatencyMs: time.Since(start).Milliseconds(), Message: "Connected to " + p.ServerVersion}, nil
}

// Connect opens (or reopens) the pool for a saved connection. password is used
// for PasswordMode "ask" (ignored otherwise).
func (m *Manager) Connect(ctx context.Context, id string, password string) (model.ConnectionStatus, error) {
	cfg, err := m.store.Connection(id)
	if err != nil {
		return model.ConnectionStatus{ConnID: id}, err
	}
	var explicit *string
	if cfg.PasswordMode == model.PasswordAsk || password != "" {
		explicit = &password // "ask" mode, or a save-mode password that is not saved (anymore)
	}
	pw, err := m.resolvePassword(cfg, explicit)
	if err != nil {
		return m.fail(cfg, err)
	}
	m.emit(model.ConnectionStatus{ConnID: id, Connecting: true, ReadOnly: cfg.ReadOnly})
	p, err := m.openWith(ctx, cfg, pw, true)
	if err != nil {
		return m.fail(cfg, err)
	}
	m.replace(id, p)
	st := statusOf(p)
	m.emit(st)
	return st, nil
}

func (m *Manager) replace(id string, p *Pool) {
	m.mu.Lock()
	old := m.pools[id]
	m.pools[id] = p
	delete(m.errors, id)
	m.mu.Unlock()
	if old != nil {
		if m.OnClose != nil {
			m.OnClose(id)
		}
		old.Close()
	}
}

// tunnelLost drops a pool whose SSH tunnel died; the next call reconnects.
func (m *Manager) tunnelLost(id string, p *Pool) {
	m.mu.Lock()
	if m.pools[id] != p {
		m.mu.Unlock()
		return
	}
	delete(m.pools, id)
	m.errors[id] = "SSH tunnel lost"
	m.mu.Unlock()
	if m.OnClose != nil {
		m.OnClose(id)
	}
	p.Close()
	m.emit(model.ConnectionStatus{ConnID: id, ReadOnly: p.Config.ReadOnly, Error: model.ErrSSH + ": SSH tunnel lost"})
}

// UseDatabase reopens an open connection on another database (postgres) or with
// another default schema (mysql). Not persisted; a reconnect returns to the saved one.
func (m *Manager) UseDatabase(ctx context.Context, id, database string) (model.ConnectionStatus, error) {
	p, err := m.Get(ctx, id)
	if err != nil {
		return model.ConnectionStatus{ConnID: id}, err
	}
	if p.Config.Driver == model.DriverSQLite {
		return statusOf(p), model.Errf(model.ErrInvalid, "SQLite has a single database per file")
	}
	cfg := p.Config
	cfg.Database = database
	if cfg.Driver == model.DriverPostgres {
		cfg.DefaultSchema = ""
	}
	np, err := m.openWith(ctx, cfg, p.password, true)
	if err != nil {
		return statusOf(p), err
	}
	m.replace(id, np)
	st := statusOf(np)
	m.emit(st)
	return st, nil
}

func (m *Manager) fail(cfg model.ConnectionConfig, err error) (model.ConnectionStatus, error) {
	m.mu.Lock()
	m.errors[cfg.ID] = err.Error()
	m.mu.Unlock()
	st := model.ConnectionStatus{ConnID: cfg.ID, ReadOnly: cfg.ReadOnly, Error: err.Error()}
	m.emit(st)
	return st, err
}

func statusOf(p *Pool) model.ConnectionStatus {
	return model.ConnectionStatus{
		ConnID: p.Config.ID, Connected: true, ServerVersion: p.ServerVersion,
		Database: p.Database, DefaultSchema: p.DefaultSchema, ReadOnly: p.Config.ReadOnly,
	}
}

func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	p := m.pools[id]
	delete(m.pools, id)
	delete(m.errors, id)
	m.mu.Unlock()
	if p != nil {
		if m.OnClose != nil {
			m.OnClose(id)
		}
		p.Close()
		m.emit(model.ConnectionStatus{ConnID: id, ReadOnly: p.Config.ReadOnly})
	}
}

// Get returns the open pool. Connections with PasswordMode "save" are connected
// on demand; "ask" connections return a password_required error until Connect.
func (m *Manager) Get(ctx context.Context, id string) (*Pool, error) {
	m.mu.Lock()
	p := m.pools[id]
	m.mu.Unlock()
	if p != nil {
		return p, nil
	}
	cfg, err := m.store.Connection(id)
	if err != nil {
		return nil, err
	}
	if cfg.PasswordMode == model.PasswordAsk {
		return nil, model.Errf(model.ErrPasswordNeeded, "connection %q needs a password; call Connect first", cfg.Name)
	}
	// Parallel first calls (sidebar + grid) must not open two pools.
	mu, _ := m.connMu.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	m.mu.Lock()
	p = m.pools[id]
	m.mu.Unlock()
	if p != nil {
		return p, nil
	}
	if _, err := m.Connect(ctx, id, ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p = m.pools[id]; p == nil {
		return nil, model.Errf(model.ErrNotConnected, "connection %q is not connected", cfg.Name)
	}
	return p, nil
}

// Statuses returns the status of every saved connection.
func (m *Manager) Statuses() ([]model.ConnectionStatus, error) {
	cfgs, err := m.store.Connections("")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.ConnectionStatus, 0, len(cfgs))
	for _, c := range cfgs {
		if p := m.pools[c.ID]; p != nil {
			out = append(out, statusOf(p))
		} else {
			out = append(out, model.ConnectionStatus{ConnID: c.ID, ReadOnly: c.ReadOnly, Error: m.errors[c.ID]})
		}
	}
	return out, nil
}

// Reconfigured closes the pool after its config changed (it reopens on demand).
func (m *Manager) Reconfigured(id string) { m.Disconnect(id) }

// CloseAll closes every pool (app shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.pools {
		p.Close()
		delete(m.pools, id)
	}
}
