package classifier

import (
	"errors"
	"testing"
)

func TestShadowEligibilityIsBoundToRelease(t *testing.T) {
	ledger, err := NewShadowLedger("release-v8")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Add(ShadowSample{ClassifierReleaseID: "release-v7", EventID: "event-1"}); !errors.Is(err, ErrReleaseMismatch) {
		t.Fatalf("mismatched release error = %v", err)
	}
	if err := ledger.Add(ShadowSample{ClassifierReleaseID: "release-v8", EventID: "event-1", SuggestedDrop: true, Reviewed: true}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Add(ShadowSample{ClassifierReleaseID: "release-v8", EventID: "event-1"}); !errors.Is(err, ErrDuplicateShadowEvent) {
		t.Fatalf("duplicate event error = %v", err)
	}

	stats := ledger.Stats()
	if stats.ClassifierReleaseID != "release-v8" || stats.ShadowEvents != 1 || stats.ReviewedSuggestedDrop != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if !(EligibilityRequirement{MinShadowEvents: 1, MinReviewedSuggestedDrop: 1}).Eligible(stats) {
		t.Fatal("release-v8 should be eligible")
	}
	if (EligibilityRequirement{MinShadowEvents: 2, MinReviewedSuggestedDrop: 1}).Eligible(stats) {
		t.Fatal("release-v8 should not borrow a sample from release-v7")
	}
}
