//go:build windows

package adminreset

import (
	"os"

	"golang.org/x/sys/windows"
)

// ReadPassword disables console echo for one line and restores the original
// console mode even when reading fails. Passwords remain stdin-only and are
// never accepted as command-line arguments.
func ReadPassword(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, os.ErrInvalid
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	muted := mode &^ windows.ENABLE_ECHO_INPUT
	if err := windows.SetConsoleMode(handle, muted); err != nil {
		return nil, err
	}
	defer func() { _ = windows.SetConsoleMode(handle, mode) }()
	return readPasswordLine(file)
}
