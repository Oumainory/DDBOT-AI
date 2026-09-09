package auth

import (
	"strings"
	"testing"
)

func TestPasswordPolicyDoesNotTrimMeaningfulWhitespace(t *testing.T) {
	valid := "  twelve chars  "
	if err := ValidatePassword(valid); err != nil {
		t.Fatalf("ValidatePassword(%q) = %v", valid, err)
	}
	if err := ValidatePassword("            "); err != ErrPasswordPolicy {
		t.Fatalf("all-whitespace password error = %v, want policy error", err)
	}
	if err := ValidatePassword("short"); err != ErrPasswordPolicy {
		t.Fatalf("short password error = %v, want policy error", err)
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordBytes+1)); err != ErrPasswordPolicy {
		t.Fatalf("oversized password error = %v, want policy error", err)
	}
}

func TestArgon2IDHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("encoded hash = %q", first)
	}
	if first == second {
		t.Fatal("two password hashes reused the salt")
	}
	if ok, err := VerifyPassword(password, first); err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	if ok, err := VerifyPassword("wrong password value", first); err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
	if ok, err := VerifyPassword(password, first+"$"); err != ErrPasswordHash || ok {
		t.Fatalf("malformed hash = %v, %v", ok, err)
	}
}
