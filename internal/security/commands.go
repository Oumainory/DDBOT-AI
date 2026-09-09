// Package security describes the boundary between authentication, setup, and
// authenticated domain commands. The HTTP layer can use this contract without
// accidentally applying CSRF or idempotency requirements to login/bootstrap.
package security

import (
	"errors"
	"fmt"
)

type Command string

const (
	CommandCreateSubscription Command = "create_subscription"
	CommandDeleteSubscription Command = "delete_subscription"
	CommandCreateTarget       Command = "create_target"
	CommandManualReplay       Command = "manual_replay"
	CommandManualRetry        Command = "manual_retry"
	CommandMigrationCommit    Command = "connector_migration_commit"
	CommandToggleEnforce      Command = "toggle_enforce"
	CommandModifyProfile      Command = "modify_profile"
	CommandModifyCredentials  Command = "modify_credentials"
	CommandTestConnection     Command = "test_connection"
	CommandAuthenticatedRead  Command = "authenticated_read"
	CommandLogin              Command = "auth_login"
	CommandSetup              Command = "setup"
)

type Protection struct {
	RequireCSRF        bool
	RequireIdempotency bool
	RequireSetupToken  bool
	RequireOrigin      bool
	OneTimeSetupToken  bool
}

var ErrUnknownCommand = errors.New("security: unknown command")

// ProtectionFor returns the explicit request protection contract for a
// command. Authentication and first-run setup intentionally have separate
// protection paths and do not require a pre-existing CSRF session.
func ProtectionFor(command Command) (Protection, error) {
	switch command {
	case CommandLogin:
		return Protection{}, nil
	case CommandSetup:
		return Protection{RequireSetupToken: true, RequireOrigin: true, OneTimeSetupToken: true}, nil
	case CommandAuthenticatedRead, CommandTestConnection:
		return Protection{}, nil
	case CommandCreateSubscription,
		CommandDeleteSubscription,
		CommandCreateTarget,
		CommandManualReplay,
		CommandManualRetry,
		CommandMigrationCommit,
		CommandToggleEnforce,
		CommandModifyProfile,
		CommandModifyCredentials:
		return Protection{RequireCSRF: true, RequireIdempotency: isReplayable(command)}, nil
	default:
		return Protection{}, fmt.Errorf("%w: %q", ErrUnknownCommand, command)
	}
}

func isReplayable(command Command) bool {
	switch command {
	case CommandCreateSubscription,
		CommandDeleteSubscription,
		CommandCreateTarget,
		CommandManualReplay,
		CommandManualRetry,
		CommandMigrationCommit,
		CommandToggleEnforce,
		CommandModifyProfile,
		CommandModifyCredentials:
		return true
	default:
		return false
	}
}
