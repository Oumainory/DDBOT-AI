package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Oumainory/DDBOT-AI/internal/domain"
	"github.com/Oumainory/DDBOT-AI/internal/pairing"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

type migrationCreateRequest struct {
	OldConnectorID string `json:"old_connector_id"`
	NewConnectorID string `json:"new_connector_id"`
}

type migrationMappingRequest struct {
	Mappings []platformdb.ConnectorMigrationMapping `json:"mappings"`
}

type pairingCreateResponse struct {
	Challenge platformdb.PairingChallenge `json:"challenge"`
	Code      string                      `json:"code,omitempty"`
}

type pairingVerifyRequest struct {
	ChallengeID string         `json:"challenge_id"`
	Code        string         `json:"code"`
	TargetType  string         `json:"target_type"`
	ExternalID  string         `json:"external_id"`
	DisplayName string         `json:"display_name"`
	Metadata    map[string]any `json:"metadata"`
}

func (s *Server) migrationAvailable(w http.ResponseWriter) bool {
	if s == nil || s.migrationRepository == nil || s.migrationCoordinator == nil {
		s.writeError(w, http.StatusServiceUnavailable, "migration_unavailable", "migration service is unavailable")
		return false
	}
	return true
}

func (s *Server) ensureTargetMutationAllowed(ctx context.Context, targetID string) error {
	if s == nil || s.migrationRepository == nil {
		return nil
	}
	frozen, err := s.migrationRepository.FrozenTarget(ctx, targetID)
	if err != nil {
		// The migration journal is an optional platform projection. A read
		// failure here must not turn a SQLite degradation into a Legacy write
		// outage; the coordinator/health surface reports the degraded state and
		// a later request can retry the gate.
		return nil
	}
	if frozen {
		return platformdb.ErrMigrationInProgress
	}
	return nil
}

func (s *Server) handleMigrations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.RequireAuth(http.HandlerFunc(s.listMigrations)).ServeHTTP(w, r)
	case http.MethodPost:
		s.RequireMutation(http.HandlerFunc(s.createMigration)).ServeHTTP(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) listMigrations(w http.ResponseWriter, r *http.Request) {
	if !s.migrationAvailable(w) {
		return
	}
	values, err := s.migrationRepository.ListMigrations(r.Context())
	if err != nil {
		s.writeMigrationError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
}

func (s *Server) createMigration(w http.ResponseWriter, r *http.Request) {
	if !s.migrationAvailable(w) {
		return
	}
	var req migrationCreateRequest
	body, err := readJSONBody(r, &req)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_argument", "migration request is invalid")
		return
	}
	s.executeDomainCommand(w, r, "create_connector_migration", body, func() (int, apiEnvelope) {
		value, err := s.migrationCoordinator.Create(r.Context(), req.OldConnectorID, req.NewConnectorID)
		if err != nil {
			return migrationErrorEnvelope(err)
		}
		return http.StatusCreated, apiEnvelope{Data: value}
	})
}

func (s *Server) handleMigrationSubresource(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v2/connector-migrations/")
	if trimmed == r.URL.Path {
		return false
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
		id := parts[0]
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			value, err := s.migrationRepository.Migration(r.Context(), id)
			if err != nil {
				s.writeMigrationError(w, err)
				return
			}
			mappings, mappingErr := s.migrationRepository.Mappings(r.Context(), id)
			if mappingErr != nil {
				s.writeMigrationError(w, mappingErr)
				return
			}
			holds, holdErr := s.migrationRepository.HoldSummary(r.Context(), id)
			if holdErr != nil {
				s.writeMigrationError(w, holdErr)
				return
			}
			auditEntries, auditErr := s.migrationRepository.ListAudit(r.Context(), "connector_migration", id)
			if auditErr != nil {
				s.writeMigrationError(w, auditErr)
				return
			}
			s.writeJSON(w, http.StatusOK, apiEnvelope{Data: value, Meta: map[string]any{"mappings": mappings, "held_summary": holds, "audit": auditEntries}})
		})).ServeHTTP(w, r)
		return true
	}
	if len(parts) < 2 || parts[0] == "" {
		return false
	}
	id := parts[0]
	switch parts[1] {
	case "discover":
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			body, err := readBoundedBody(r)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "migration request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "migration_discover", body, func() (int, apiEnvelope) {
				values, err := s.migrationCoordinator.Discover(r.Context(), id)
				if err != nil {
					return migrationErrorEnvelope(err)
				}
				return http.StatusOK, apiEnvelope{Data: map[string]any{"migration_id": id, "mappings": values}}
			})
		})).ServeHTTP(w, r)
		return true
	case "preflight":
		if r.Method != http.MethodPost {
			s.writeError(w, 405, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			body, err := readBoundedBody(r)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "migration request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "migration_preflight", body, func() (int, apiEnvelope) {
				value, err := s.migrationCoordinator.Preflight(r.Context(), id)
				if err != nil {
					return migrationErrorEnvelope(err)
				}
				return http.StatusOK, apiEnvelope{Data: value}
			})
		})).ServeHTTP(w, r)
		return true
	case "mappings":
		if r.Method == http.MethodGet {
			s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !s.migrationAvailable(w) {
					return
				}
				values, err := s.migrationRepository.Mappings(r.Context(), id)
				if err != nil {
					s.writeMigrationError(w, err)
					return
				}
				s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
			})).ServeHTTP(w, r)
			return true
		}
		if r.Method != http.MethodPut && r.Method != http.MethodPatch {
			s.writeError(w, 405, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			var req migrationMappingRequest
			body, err := readJSONBody(r, &req)
			if err != nil {
				s.writeError(w, 400, "invalid_argument", "mapping request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "migration_mapping_update", body, func() (int, apiEnvelope) {
				if err := s.migrationCoordinator.SetMappings(r.Context(), id, req.Mappings); err != nil {
					return migrationErrorEnvelope(err)
				}
				values, _ := s.migrationRepository.Mappings(r.Context(), id)
				return http.StatusOK, apiEnvelope{Data: map[string]any{"mappings": values}}
			})
		})).ServeHTTP(w, r)
		return true
	case "commit":
		if r.Method != http.MethodPost {
			s.writeError(w, 405, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			body, err := readBoundedBody(r)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "migration request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "migration_commit", body, func() (int, apiEnvelope) {
				value, err := s.migrationCoordinator.Commit(r.Context(), id)
				if err != nil {
					return migrationErrorEnvelope(err)
				}
				return http.StatusOK, apiEnvelope{Data: value}
			})
		})).ServeHTTP(w, r)
		return true
	case "rollback":
		if r.Method != http.MethodPost {
			s.writeError(w, 405, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			body, err := readBoundedBody(r)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "migration request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "migration_rollback", body, func() (int, apiEnvelope) {
				value, err := s.migrationCoordinator.Rollback(r.Context(), id)
				if err != nil {
					return migrationErrorEnvelope(err)
				}
				return http.StatusOK, apiEnvelope{Data: value}
			})
		})).ServeHTTP(w, r)
		return true
	case "held-deliveries":
		if r.Method != http.MethodGet {
			s.writeError(w, 405, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			values, err := s.migrationRepository.Holds(r.Context(), id)
			if err != nil {
				s.writeMigrationError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
		})).ServeHTTP(w, r)
		return true
	case "audit":
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return true
		}
		s.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.migrationAvailable(w) {
				return
			}
			values, err := s.migrationRepository.ListAudit(r.Context(), "connector_migration", id)
			if err != nil {
				s.writeMigrationError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, apiEnvelope{Data: map[string]any{"items": values}})
		})).ServeHTTP(w, r)
		return true
	}
	return false
}

func migrationErrorEnvelope(err error) (int, apiEnvelope) {
	switch {
	case errors.Is(err, platformdb.ErrMigrationNotFound):
		return 404, apiEnvelope{Error: &apiError{Code: "migration_not_found", Message: "migration not found"}}
	case errors.Is(err, platformdb.ErrMigrationInProgress):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_in_progress", Message: "another migration is in progress"}}
	case errors.Is(err, platformdb.ErrMigrationMappingRequired):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_mapping_required", Message: "explicit target mapping is required"}}
	case errors.Is(err, platformdb.ErrMigrationMappingAmbiguous):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_mapping_ambiguous", Message: "target mapping is ambiguous"}}
	case errors.Is(err, platformdb.ErrMigrationTargetUnavailable):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_target_unavailable", Message: "mapped target is unavailable"}}
	case errors.Is(err, platformdb.ErrMigrationRecovery):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_recovery_required", Message: "migration requires recovery"}}
	case errors.Is(err, platformdb.ErrMigrationRollbackExpired):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_rollback_expired", Message: "rollback window has expired"}}
	case errors.Is(err, platformdb.ErrMigrationPreflight):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_preflight_failed", Message: "migration preflight failed"}}
	case errors.Is(err, platformdb.ErrMigrationInvalidState):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_invalid_state", Message: "migration state does not allow this operation"}}
	case errors.Is(err, platformdb.ErrMigrationHoldConflict):
		return 409, apiEnvelope{Error: &apiError{Code: "migration_hold_conflict", Message: "delivery is already held by another migration"}}
	case errors.Is(err, platformdb.ErrPairingInvalid):
		return 409, apiEnvelope{Error: &apiError{Code: "pairing_invalid", Message: "pairing challenge is invalid"}}
	case errors.Is(err, platformdb.ErrPairingExpired):
		return 409, apiEnvelope{Error: &apiError{Code: "pairing_expired", Message: "pairing challenge is expired"}}
	case errors.Is(err, platformdb.ErrPairingLocked):
		return 409, apiEnvelope{Error: &apiError{Code: "pairing_locked", Message: "pairing challenge is locked"}}
	case errors.Is(err, platformdb.ErrPairingConsumed):
		return 409, apiEnvelope{Error: &apiError{Code: "pairing_consumed", Message: "pairing challenge is already consumed"}}
	default:
		return errorEnvelope(err)
	}
}

func (s *Server) writeMigrationError(w http.ResponseWriter, err error) {
	status, response := migrationErrorEnvelope(err)
	s.writeJSON(w, status, response)
}

func (s *Server) handlePairingSubresource(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v2/connectors/")
	if trimmed == r.URL.Path {
		return false
	}
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) < 2 || parts[1] != "pairing" {
		return false
	}
	connectorID := parts[0]
	if len(parts) == 2 && r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.pairingService == nil {
				s.writeError(w, 503, "pairing_unavailable", "pairing service is unavailable")
				return
			}
			body, err := readBoundedBody(r)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_argument", "pairing request is invalid")
				return
			}
			s.executeSanitizedReplayCommand(w, r, "telegram_pairing_create", body, func() (int, apiEnvelope) {
				principal, _ := PrincipalFromContext(r.Context())
				created, err := s.pairingService.Create(r.Context(), connectorID, principal.AdminID, "")
				if err != nil {
					return migrationErrorEnvelope(err)
				}
				return http.StatusCreated, apiEnvelope{Data: pairingCreateResponse{Challenge: created.Challenge, Code: created.Code}}
			}, stripPairingPlaintext)
		})).ServeHTTP(w, r)
		return true
	}
	// Verification is addressed by the connector, pairing challenge and
	// explicit action: /connectors/{connector}/pairing/{challenge}/verify.
	// The challenge ID is deliberately not accepted from the request body so
	// it cannot be confused with the one-time code itself.
	if ((len(parts) == 4 && parts[3] == "verify") || (len(parts) == 3 && parts[2] == "verify")) && r.Method == http.MethodPost {
		s.RequireMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.pairingService == nil || s.pairingVerifier == nil {
				s.writeError(w, 409, "telegram_verification_failed", "telegram target verification failed")
				return
			}
			var req pairingVerifyRequest
			body, err := readJSONBody(r, &req)
			if err != nil {
				s.writeError(w, 400, "invalid_argument", "pairing request is invalid")
				return
			}
			s.executeDomainCommand(w, r, "telegram_pairing_verify", body, func() (int, apiEnvelope) {
				verification := pairing.Verification{TargetType: domain.TargetType(req.TargetType), ExternalID: req.ExternalID, DisplayName: req.DisplayName, Metadata: req.Metadata}
				var target domain.Target
				var verifyErr error
				if len(parts) == 4 {
					target, verifyErr = s.pairingService.VerifyTargetForConnector(r.Context(), connectorID, parts[2], req.Code, verification, s.pairingVerifier)
				} else if strings.TrimSpace(req.ChallengeID) != "" {
					target, verifyErr = s.pairingService.VerifyTargetForConnector(r.Context(), connectorID, req.ChallengeID, req.Code, verification, s.pairingVerifier)
				} else {
					target, verifyErr = s.pairingService.VerifyTargetByCodeForConnector(r.Context(), connectorID, req.Code, verification, s.pairingVerifier)
				}
				if verifyErr != nil {
					return http.StatusConflict, apiEnvelope{Error: &apiError{Code: "telegram_verification_failed", Message: "telegram target verification failed"}}
				}
				return http.StatusOK, apiEnvelope{Data: target}
			})
		})).ServeHTTP(w, r)
		return true
	}
	return false
}
