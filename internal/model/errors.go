package model

import (
	"errors"
	"fmt"
)

// Error codes. A bound method that fails rejects its JS promise with the string
// "<code>: <message>" (e.g. "read_only: connection \"prod\" is read-only").
// frontend/src/api parses it into {code, message}.
const (
	ErrNotFound       = "not_found"
	ErrInvalid        = "invalid_argument"
	ErrNotConnected   = "not_connected"
	ErrReadOnly       = "read_only"
	ErrNoPrimaryKey   = "no_primary_key"
	ErrCanceled       = "canceled"
	ErrPasswordNeeded = "password_required"
	ErrKeychain       = "keychain" // no longer produced (the Keychain is not used)
	ErrDB             = "db_error"
	ErrNotImplemented = "not_implemented"
	ErrInternal       = "internal"
	ErrUnsupported    = "unsupported" // e.g. native dialogs in browser dev mode
	// SSH tunnel errors
	ErrSSH             = "ssh_error"
	ErrHostKeyUnknown  = "host_key_unknown"  // first contact: confirm with TrustHostKey, then retry
	ErrHostKeyMismatch = "host_key_mismatch" // key changed: hard failure
	// an encrypted SSH key the server accepts needs its passphrase:
	// ProvideKeyPassphrase, then retry
	ErrPassphraseRequired = "passphrase_required"
)

// AppError is an error with a stable code.
// Wails v3 rejects the JS promise with an error whose `cause` is this struct
// marshalled to JSON ({code, message}); err.message is "<code>: <message>".
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string { return e.Code + ": " + e.Message }
func (e *AppError) Unwrap() error { return e.Err }

// Errf builds an AppError.
func Errf(code, format string, args ...any) error {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap wraps err with a code; nil stays nil. AppErrors pass through unchanged.
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return err
	}
	return &AppError{Code: code, Message: err.Error(), Err: err}
}

// NotImplemented is returned by stubs that are not built yet.
func NotImplemented(what string) error {
	return Errf(ErrNotImplemented, "%s is not implemented yet", what)
}
