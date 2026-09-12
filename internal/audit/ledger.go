// Package audit exposes the append-only application audit boundary. It is a
// thin policy wrapper over platformdb; there is intentionally no update or
// single-row delete API.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

type Entry = platformdb.AuditEntry

type Ledger struct {
	repository *platformdb.MigrationRepository
	now        func() time.Time
}

func New(repository *platformdb.MigrationRepository, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{repository: repository, now: now}
}

func (l *Ledger) Append(ctx context.Context, entry Entry) (Entry, error) {
	if entry.OccurredAt == 0 {
		entry.OccurredAt = l.now().UTC().Unix()
	}
	if entry.MetadataJSON == "" {
		entry.MetadataJSON = "{}"
	}
	return l.repository.AppendAudit(ctx, entry)
}
func (l *Ledger) AppendSafe(ctx context.Context, principal, action, resourceType, resourceID, outcome, requestID, idempotency string, metadata map[string]any) (Entry, error) {
	raw := "{}"
	if metadata != nil {
		b, _ := json.Marshal(metadata)
		raw = string(b)
	}
	return l.Append(ctx, Entry{PrincipalID: principal, Action: action, ResourceType: resourceType, ResourceID: resourceID, Outcome: outcome, RequestID: requestID, IdempotencyKey: idempotency, MetadataJSON: raw})
}
func (l *Ledger) List(ctx context.Context, resourceType, resourceID string) ([]Entry, error) {
	return l.repository.ListAudit(ctx, resourceType, resourceID)
}
func (l *Ledger) VerifyAuditChain(ctx context.Context) error {
	return l.repository.VerifyAuditChain(ctx)
}

// Prune applies the repository's unified retention boundary. The operation
// writes an audit.prune anchor and re-chains retained entries atomically.
func (l *Ledger) Prune(ctx context.Context, cutoff time.Time) (int, error) {
	return l.repository.PruneAuditAt(ctx, cutoff, l.now())
}
