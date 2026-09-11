//go:build !windows

package adminreset

import (
	"os"

	"golang.org/x/sys/unix"
)

// ReadPassword disables terminal echo for one line and restores the original
// terminal mode even when reading fails. The fallback is still stdin-only;
// passwords are never accepted as command-line arguments.
func ReadPassword(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, os.ErrInvalid
	}
	fd := int(file.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	muted := *state
	muted.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &muted); err != nil {
		return nil, err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, state) }()
	return readPasswordLine(file)
}
