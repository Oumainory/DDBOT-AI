package auth

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrUsernamePolicy = errors.New("auth: invalid username")

// NormalizeUsername makes lookup case-insensitive without changing password
// semantics. It deliberately accepts non-ASCII UTF-8 usernames while
// rejecting controls and embedded whitespace.
func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" || len(username) > 128 || !utf8.ValidString(username) {
		return "", ErrUsernamePolicy
	}
	for _, r := range username {
		if r < 0x20 || r == 0x7f || r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return "", ErrUsernamePolicy
		}
	}
	return username, nil
}
