package platformdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/policy"
)

// seedReadyApprovalEvidence creates the smallest durable dataset that meets
// every V1 readiness threshold. It intentionally uses SQL only for test data;
// production approval creation still goes through the repository command.
func seedReadyApprovalEvidence(t *testing.T, store *Store, release classifier.Release, at time.Time) {
	t.Helper()
	if err := NewAIRepository(store).SaveRelease(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	caseIDs := make([]string, 100)
	for i := range caseIDs {
		caseIDs[i] = fmt.Sprintf("approval-case-%03d", i)
		importance := "low"
		if i < 40 {
			importance = "high"
		}
		mustExec(t, store.db, `INSERT INTO ai_evaluation_cases (id,normalized_input_snapshot_json,normalized_schema_version,expected_importance,expected_action,critical,label_kind,notes,source,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, caseIDs[i], `{"schema_version":1}`, 1, importance, "pass", 0, "real_reviewed", "", "test", at.Unix(), at.Unix())
	}
	mustExec(t, store.db, `INSERT INTO ai_evaluation_runs (id,classifier_release_id,status,case_ids_json,metrics_json,total_cost_micros,latency_ms,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?)`, "approval-evaluation-run", release.ID, "completed", "[]", "{}", 0, 0, at.Unix(), at.Unix())
	for i, caseID := range caseIDs {
		mustExec(t, store.db, `INSERT INTO ai_evaluation_results (id,run_id,case_id,classification_json,suggested_action,expected_action,comparison,cost_micros,latency_ms,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("approval-result-%03d", i), "approval-evaluation-run", caseID, `{}`, "pass", "pass", "match", 0, 0, at.Unix())
	}
	for i := 0; i < 200; i++ {
		eventID := fmt.Sprintf("approval-shadow-event-%03d", i)
		decisionID := fmt.Sprintf("approval-shadow-decision-%03d", i)
		mustExec(t, store.db, `INSERT INTO normalized_events (id,schema_version,observed_event_id,platform,source_id,external_id,event_type,author_id,author_name,source_display_name,title,body,related_body,public_url,public_urls_json,media_json,source_event_at,observed_at,normalizer_version,preprocessor_version,truncated,normalization_flags_json,snapshot_json,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, eventID, 1, "observed-"+eventID, "bilibili", "source", eventID, "dynamic", "", "", "", "", "public", "", "https://example.test/"+eventID, "[]", "[]", at.Unix(), at.Unix(), "test", "test", 0, "[]", `{}`, at.Unix())
		suggested := "pass"
		reviewed := 0
		if i < 50 {
			suggested = "drop"
			reviewed = 1
		}
		mustExec(t, store.db, `INSERT INTO ai_decisions (id,normalized_event_id,classifier_release_id,status,mode_at_schedule,classification_json,suggested_action,effective_action,hard_pass_reason,provider,model,input_tokens,output_tokens,total_tokens,cost_micros,cost_currency,latency_ms,error_code,scheduled_at,call_started_at,completed_at,reviewed,reviewed_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, decisionID, eventID, release.ID, "completed", "shadow", `{}`, suggested, "pass", "", "test", "test", 0, 0, 0, 0, "", 0, "", at.Unix(), at.Unix(), at.Unix(), reviewed, at.Unix(), at.Unix())
	}
}

func readyApprovalCommand(releaseID string, at time.Time) EnforceApprovalCommand {
	return EnforceApprovalCommand{
		ID:                "approval-command-ready",
		ExpectedReleaseID: releaseID,
		PolicyOverride:    AIPolicyOverrideRecord{DefaultAction: policy.ActionPass},
		ApprovedBy:        "test-admin",
		ApprovedAt:        at,
		CreatedAt:         at,
	}
}

func TestCreateEnforceApprovalIfReadyUsesOneCanonicalTransaction(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "approval-command.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release := aiTestRelease()
	release.ID, release.Fingerprint, release.Active = "release-approval-command", "fp-approval-command", true
	seedReadyApprovalEvidence(t, store, release, at)
	repo := NewPhase5Repository(store)
	value, readiness, err := repo.CreateEnforceApprovalIfReady(ctx, readyApprovalCommand(release.ID, at))
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.CurrentReleaseID != release.ID {
		t.Fatalf("readiness=%#v", readiness)
	}
	if value.ClassifierReleaseID != release.ID || len(value.ReadinessEvidenceJSON) == 0 {
		t.Fatalf("approval=%#v", value)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT readiness_evidence_json FROM enforce_approvals WHERE id=?`, value.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != string(value.ReadinessEvidenceJSON) {
		t.Fatalf("stored canonical evidence=%s returned=%s", stored, value.ReadinessEvidenceJSON)
	}
	var decoded EnforceReadiness
	if err := json.Unmarshal(value.ReadinessEvidenceJSON, &decoded); err != nil || decoded.CurrentReleaseID != release.ID || !decoded.Ready {
		t.Fatalf("canonical evidence=%s err=%v", value.ReadinessEvidenceJSON, err)
	}
	if _, err := repo.ValidEnforceApproval(ctx, release.ID, value.PolicyDigest, value.ProfileDigest, at); err != nil {
		t.Fatalf("canonical approval read-back=%v", err)
	}
}

func TestCreateEnforceApprovalIfReadyRejectsReadinessMutationBeforeCommand(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "approval-command-race.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release := aiTestRelease()
	release.ID, release.Fingerprint, release.Active = "release-approval-race", "fp-approval-race", true
	seedReadyApprovalEvidence(t, store, release, at)
	// Commit a new current-release important false-drop before the approval
	// command obtains its transaction. The command must observe the same durable
	// evidence and refuse to create an approval; it may not reuse an earlier
	// readiness snapshot.
	badRun := "approval-bad-run"
	badResult := "approval-bad-result"
	mustExec(t, store.db, `INSERT INTO ai_evaluation_runs (id,classifier_release_id,status,case_ids_json,metrics_json,total_cost_micros,latency_ms,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?)`, badRun, release.ID, "completed", "[]", "{}", 0, 0, at.Add(time.Second).Unix(), at.Add(time.Second).Unix())
	mustExec(t, store.db, `INSERT INTO ai_evaluation_results (id,run_id,case_id,classification_json,suggested_action,expected_action,comparison,cost_micros,latency_ms,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, badResult, badRun, "approval-case-000", `{}`, "drop", "pass", "mismatch", 0, 0, at.Add(time.Second).Unix())
	_, _, err = NewPhase5Repository(store).CreateEnforceApprovalIfReady(ctx, readyApprovalCommand(release.ID, at.Add(2*time.Second)))
	if !errors.Is(err, ErrEnforceNotReady) {
		t.Fatalf("approval after readiness mutation=%v, want %v", err, ErrEnforceNotReady)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM enforce_approvals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale approval rows=%d", count)
	}
}

func TestCreateEnforceApprovalIfReadyReleaseActivationInterleave(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "approval-release-interleave.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	releaseA := aiTestRelease()
	releaseA.ID, releaseA.Fingerprint, releaseA.Active = "release-interleave-a", "fp-interleave-a", true
	seedReadyApprovalEvidence(t, store, releaseA, at)
	releaseB := aiTestRelease()
	releaseB.ID, releaseB.Fingerprint, releaseB.Active = "release-interleave-b", "fp-interleave-b", false
	ai := NewAIRepository(store)
	if err := ai.SaveRelease(ctx, releaseB); err != nil {
		t.Fatal(err)
	}
	repo := NewPhase5Repository(store)
	var wg sync.WaitGroup
	var approvalErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, approvalErr = repo.CreateEnforceApprovalIfReady(ctx, readyApprovalCommand(releaseA.ID, at))
	}()
	go func() {
		defer wg.Done()
		if err := ai.ActivateRelease(ctx, releaseB.ID); err != nil {
			t.Errorf("activate release B: %v", err)
		}
	}()
	wg.Wait()
	if approvalErr != nil && !errors.Is(approvalErr, ErrEnforceNotReady) && !errors.Is(approvalErr, ErrApprovalInvalid) {
		t.Fatalf("approval interleave error=%v", approvalErr)
	}
	if _, err := repo.ValidEnforceApproval(ctx, releaseA.ID, policy.Digest(policy.EffectivePolicy{Mode: policy.ModeEnforce, Threshold: .90, CategoryActions: policy.OfficialGameProfile().CategoryActions, TagActions: policy.OfficialGameProfile().TagActions}), policy.ProfileDigest(policy.OfficialGameProfile()), at.Add(time.Hour)); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("stale release A approval=%v, want not found after activation", err)
	}
}
