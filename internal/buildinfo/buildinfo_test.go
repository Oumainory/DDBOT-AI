package buildinfo

import "testing"

func TestCurrentUsesExplicitBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = oldVersion, oldCommit, oldBuildTime })
	Version, Commit, BuildTime = "v1.2.3", "0123456789abcdef0123456789abcdef01234567", "2026-09-11T00:00:00Z"
	info := Current()
	if info.ProductName != ProductName || info.Version != Version || info.Commit != Commit || info.BuildTime != BuildTime {
		t.Fatalf("Current() = %#v", info)
	}
	if got, want := info.CommitURL(), SourceRepository+"/commit/"+Commit; got != want {
		t.Fatalf("CommitURL() = %q, want %q", got, want)
	}
}

func TestCommitURLRejectsNonSHAValues(t *testing.T) {
	for _, commit := range []string{"unknown", "dev", "short", "not-a-sha-1234567"} {
		if got := (Info{Commit: commit}).CommitURL(); got != "" {
			t.Fatalf("CommitURL(%q) = %q, want empty", commit, got)
		}
	}
}
