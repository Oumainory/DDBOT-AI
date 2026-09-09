package compat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type manifest struct {
	SchemaVersion      int       `json:"schema_version"`
	Product            string    `json:"product"`
	BaselineRepository string    `json:"baseline_repository"`
	BaselineCommit     string    `json:"baseline_commit"`
	BaselineAncestor   bool      `json:"baseline_ancestor_required"`
	CompatibilityCmd   string    `json:"compatibility_command"`
	Fixtures           []fixture `json:"fixtures"`
}

type fixture struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`
	FixtureFile   string   `json:"fixture_file"`
	ExistingTests []string `json:"existing_tests"`
	Observables   []string `json:"observables"`
}

func TestBaselineManifestIsComplete(t *testing.T) {
	path := filepath.Join("manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compatibility manifest: %v", err)
	}

	var got manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode compatibility manifest: %v", err)
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if got.Product != "DDBOT-AI" {
		t.Fatalf("product = %q, want DDBOT-AI", got.Product)
	}
	if got.BaselineCommit != "a6364e7182ec4eee93dd78e09fe7a7efd92bffab" {
		t.Fatalf("baseline_commit = %q, want locked upstream commit", got.BaselineCommit)
	}
	if !got.BaselineAncestor || got.CompatibilityCmd == "" {
		t.Fatalf("manifest must require an ancestor baseline and executable compatibility command")
	}
	if len(got.Fixtures) < 6 {
		t.Fatalf("fixture count = %d, want at least 6", len(got.Fixtures))
	}

	seen := make(map[string]bool, len(got.Fixtures))
	for _, fixture := range got.Fixtures {
		if fixture.ID == "" || fixture.Category == "" {
			t.Fatalf("fixture has empty id/category: %#v", fixture)
		}
		if seen[fixture.ID] {
			t.Fatalf("duplicate fixture id %q", fixture.ID)
		}
		seen[fixture.ID] = true
		if fixture.FixtureFile == "" || len(fixture.ExistingTests) == 0 || len(fixture.Observables) == 0 {
			t.Fatalf("fixture %q is missing test selectors or observables", fixture.ID)
		}
		if _, err := os.Stat(filepath.Join(fixture.FixtureFile)); err != nil {
			t.Fatalf("fixture %q points to missing file %q: %v", fixture.ID, fixture.FixtureFile, err)
		}
	}
}
