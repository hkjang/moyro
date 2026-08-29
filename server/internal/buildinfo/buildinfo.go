// Package buildinfo exposes immutable metadata injected by the release build.
// Development builds intentionally fall back to recognizable sentinel values
// instead of pretending to be a published release.
package buildinfo

import "runtime"

var (
	// Version is the release tag, for example "v1.2.3".
	Version = "dev"
	// Commit is the full source revision used for the build.
	Commit = "unknown"
	// BuildDate is an RFC 3339 UTC timestamp supplied by the build pipeline.
	BuildDate = "unknown"
)

// Info is the stable JSON-friendly shape shared by public and administrative
// version endpoints.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Current returns a snapshot so callers cannot mutate package metadata.
func Current() Info {
	return Info{
		Version:   fallback(Version, "dev"),
		Commit:    fallback(Commit, "unknown"),
		BuildDate: fallback(BuildDate, "unknown"),
		GoVersion: runtime.Version(),
	}
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
