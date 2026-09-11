// Package adminreset contains the local-only administrator password recovery
// command boundary. It has no HTTP, session-cookie, or remote API surface.
package adminreset

import (
	"context"
	"errors"
	"io"
)

var (
	ErrUnavailable      = errors.New("adminreset: unavailable")
	ErrPasswordMismatch = errors.New("adminreset: passwords do not match")
	ErrPasswordRead     = errors.New("adminreset: unable to read password")
	ErrResetFailed      = errors.New("adminreset: password reset failed")
)

// PasswordResetter is implemented by auth.Service. Keeping the command
// boundary on this small interface makes it possible to test prompts and
// secret-handling without invoking Argon2id or a real database.
type PasswordResetter interface {
	ResetAdministratorPassword(context.Context, string) error
}

// PasswordReader reads one password without echoing it. The CLI supplies a
// terminal-backed implementation; tests inject a deterministic reader.
type PasswordReader func() (string, error)

// Run prompts for and confirms a new password, then performs one local reset.
// It never writes either password, its hash, session material, or database
// details to output.
func Run(ctx context.Context, service PasswordResetter, out io.Writer, read PasswordReader) error {
	if service == nil || read == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	_, _ = io.WriteString(out, "New administrator password: ")
	first, err := read()
	if err != nil {
		return ErrPasswordRead
	}
	_, _ = io.WriteString(out, "\nConfirm administrator password: ")
	second, err := read()
	if err != nil {
		return ErrPasswordRead
	}
	if first != second {
		return ErrPasswordMismatch
	}
	if err := service.ResetAdministratorPassword(ctx, first); err != nil {
		return ErrResetFailed
	}
	_, _ = io.WriteString(out, "\nAdministrator password reset successfully.\n")
	return nil
}
