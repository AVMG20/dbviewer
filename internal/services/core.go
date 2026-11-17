// Package services holds the Wails-bound services. Methods are thin: they resolve
// the connection pool and delegate to the internal packages.
//
// Every returned error is a *model.AppError so the frontend gets {code, message}.
package services

import (
	"context"
	"errors"
	"reflect"
	"runtime"

	"dbviewer/internal/conn"
	"dbviewer/internal/meta"
	"dbviewer/internal/model"
	"dbviewer/internal/query"
	"dbviewer/internal/store"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Version is the app version shown in AppInfo.
var Version = "0.1.0-dev"

// Event names emitted by the backend.
const (
	// EventConnStatus carries a model.ConnectionStatus whenever a connection
	// connects, disconnects or fails.
	EventConnStatus = "conn:status"
	// EventSSHHostKey carries a model.HostKeyPrompt when an SSH host is unknown.
	EventSSHHostKey = "ssh:hostkey"
	// EventSSHPassphrase carries a model.SSHKeyPrompt when an encrypted SSH key
	// accepted by the server needs its passphrase.
	EventSSHPassphrase = "ssh:passphrase"
	// EventFindProgress carries a model.FindProgress during FindInDatabase.
	EventFindProgress = "find:progress"
)

func init() {
	application.RegisterEvent[model.ConnectionStatus](EventConnStatus)
	application.RegisterEvent[model.HostKeyPrompt](EventSSHHostKey)
	application.RegisterEvent[model.FindProgress](EventFindProgress)
	application.RegisterEvent[model.SSHKeyPrompt](EventSSHPassphrase)
}

func emit(name string, data any) {
	if app := application.Get(); app != nil {
		app.Event.Emit(name, data)
	}
}

// Core is shared by all services.
type Core struct {
	Store  *store.Store
	Conns  *conn.Manager
	Meta   *meta.Cache
	Runner *query.Runner
}

// NewCore opens the app-state store in the data dir and wires the managers.
func NewCore() (*Core, error) {
	dir, err := store.DataDir()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	c := &Core{Store: st, Conns: conn.NewManager(st), Meta: meta.NewCache(), Runner: query.NewRunner()}
	c.Conns.OnStatus = func(s model.ConnectionStatus) {
		emit(EventConnStatus, s)
		if !s.Connected {
			c.Meta.Invalidate(s.ConnID, "")
		}
	}
	c.Conns.OnHostKey = func(p model.HostKeyPrompt) { emit(EventSSHHostKey, p) }
	c.Conns.OnKeyPassphrase = func(p model.SSHKeyPrompt) { emit(EventSSHPassphrase, p) }
	c.Conns.OnClose = func(connID string) {
		c.Runner.CloseConnection(connID)
		c.Meta.Invalidate(connID, "")
	}
	return c, nil
}

// Close releases pools and the store (app shutdown).
func (c *Core) Close() {
	c.Conns.CloseAll()
	_ = c.Store.Close()
}

// pool resolves an open pool (connecting on demand for "save" password mode).
func (c *Core) pool(ctx context.Context, connID string) (*conn.Pool, error) {
	if connID == "" {
		return nil, model.Errf(model.ErrInvalid, "connId is required")
	}
	p, err := c.Conns.Get(ctx, connID)
	return p, wrap(err)
}

// wrap guarantees an AppError (so the frontend always receives a code).
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *model.AppError
	if errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return model.Wrap(model.ErrCanceled, err)
	}
	return model.Wrap(model.ErrInternal, err)
}

func platform() string { return runtime.GOOS }

// ret normalizes a result before it crosses the bridge: nil slices become [] and
// nil maps become {} (recursively), so the frontend never sees null arrays.
// Errors are wrapped into AppErrors.
func ret[T any](v T, err error) (T, error) {
	fillNil(reflect.ValueOf(&v).Elem())
	return v, wrap(err)
}

func fillNil(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			fillNil(v.Elem())
		}
	case reflect.Interface:
		// leave dynamic values (row cells) alone
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				fillNil(f)
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}
			return
		}
		if k := v.Type().Elem().Kind(); k == reflect.Struct || k == reflect.Slice || k == reflect.Pointer || k == reflect.Map {
			for i := 0; i < v.Len(); i++ {
				fillNil(v.Index(i))
			}
		}
	case reflect.Map:
		if v.IsNil() && v.CanSet() {
			v.Set(reflect.MakeMap(v.Type()))
		}
	}
}
