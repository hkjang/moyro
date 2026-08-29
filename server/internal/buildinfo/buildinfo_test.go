package buildinfo

import "testing"

func TestCurrentUsesInjectedValues(t *testing.T) {
	originalVersion, originalCommit, originalDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version, Commit, BuildDate = originalVersion, originalCommit, originalDate
	})

	Version = "v1.2.3"
	Commit = "abc123"
	BuildDate = "2026-08-29T00:00:00Z"

	got := Current()
	if got.Version != Version || got.Commit != Commit || got.BuildDate != BuildDate {
		t.Fatalf("Current() = %#v", got)
	}
	if got.GoVersion == "" {
		t.Fatal("GoVersion must not be empty")
	}
}

func TestCurrentFallsBackForEmptyLinkerValues(t *testing.T) {
	originalVersion, originalCommit, originalDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version, Commit, BuildDate = originalVersion, originalCommit, originalDate
	})

	Version, Commit, BuildDate = "", "", ""
	got := Current()
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildDate != "unknown" {
		t.Fatalf("Current() fallback = %#v", got)
	}
}
