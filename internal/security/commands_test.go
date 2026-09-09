package security

import (
	"errors"
	"testing"
)

func TestProtectionSeparatesAuthSetupAndDomainCommands(t *testing.T) {
	login, err := ProtectionFor(CommandLogin)
	if err != nil {
		t.Fatal(err)
	}
	if login.RequireCSRF || login.RequireIdempotency || login.RequireSetupToken {
		t.Fatalf("login protection = %#v, should not require a prior session", login)
	}

	setup, err := ProtectionFor(CommandSetup)
	if err != nil {
		t.Fatal(err)
	}
	if setup.RequireCSRF || !setup.RequireSetupToken || !setup.RequireOrigin || !setup.OneTimeSetupToken {
		t.Fatalf("setup protection = %#v, want setup token + origin + one-time consumption", setup)
	}

	command, err := ProtectionFor(CommandCreateSubscription)
	if err != nil {
		t.Fatal(err)
	}
	if !command.RequireCSRF || !command.RequireIdempotency {
		t.Fatalf("create subscription protection = %#v, want CSRF + idempotency", command)
	}

	testConnection, err := ProtectionFor(CommandTestConnection)
	if err != nil {
		t.Fatal(err)
	}
	if testConnection.RequireCSRF || testConnection.RequireIdempotency {
		t.Fatalf("test connection protection = %#v, should not require replay protection", testConnection)
	}
}

func TestProtectionRejectsUnknownCommand(t *testing.T) {
	_, err := ProtectionFor(Command("something_else"))
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("error = %v, want ErrUnknownCommand", err)
	}
}
