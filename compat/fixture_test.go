package compat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type behaviorFixture struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Baseline      string         `json:"baseline_commit"`
	Scenario      map[string]any `json:"scenario"`
	Expected      map[string]any `json:"expected"`
	Normalize     []string       `json:"normalize"`
}

// TestBehaviorFixturesAreReviewable is intentionally a schema/fixture gate,
// not a fake reimplementation of the upstream bot. The future compatibility
// runner will execute each scenario against baseline and current binaries and
// compare the normalized Expected fields. Checking the vectors in Phase 0
// prevents a hook from being added without first defining its observable.
func TestBehaviorFixturesAreReviewable(t *testing.T) {
	manifestData, err := os.ReadFile(filepath.Join("manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Fixtures {
		data, err := os.ReadFile(filepath.Join(entry.FixtureFile))
		if err != nil {
			t.Fatalf("read %s: %v", entry.FixtureFile, err)
		}
		var fixture behaviorFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("decode %s: %v", entry.FixtureFile, err)
		}
		if fixture.SchemaVersion != 1 || fixture.ID != entry.ID || fixture.Baseline != manifest.BaselineCommit {
			t.Fatalf("fixture %s has inconsistent identity: %#v", entry.ID, fixture)
		}
		if len(fixture.Scenario) == 0 || len(fixture.Expected) == 0 || len(fixture.Normalize) == 0 {
			t.Fatalf("fixture %s must define scenario, expected output, and normalization", entry.ID)
		}
	}
}
