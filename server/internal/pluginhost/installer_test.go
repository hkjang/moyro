package pluginhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type bundleEntry struct {
	header tar.Header
	body   []byte
}

func makeBundle(t *testing.T, entries ...bundleEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := entry.header
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			header.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatalf("write tar header %q: %v", header.Name, err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatalf("write tar body %q: %v", header.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return output.Bytes()
}

func dirEntry(name string) bundleEntry {
	return bundleEntry{header: tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o777, Uid: 1234, Gid: 5678}}
}

func fileEntry(name string, body []byte) bundleEntry {
	return bundleEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o777, Uid: 1234, Gid: 5678}, body: body}
}

func manifestJSON(t *testing.T, id, executable, webBundle string) []byte {
	t.Helper()
	manifest := map[string]any{
		"id":      id,
		"name":    "Test Plugin",
		"version": "1.0.0",
	}
	if executable != "" {
		manifest["server"] = map[string]any{
			"executables": map[string]string{
				runtime.GOOS + "-" + runtime.GOARCH: executable,
			},
		}
	}
	if webBundle != "" {
		manifest["webapp"] = map[string]string{"bundle_path": webBundle}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectStageError(t *testing.T, bundle []byte, limits BundleLimits, want string) {
	t.Helper()
	parent := t.TempDir()
	_, err := ExtractBundleToStagingWithLimits(context.Background(), parent, bytes.NewReader(bundle), limits)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ExtractBundleToStagingWithLimits() error = %v, want substring %q", err, want)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed extraction left staging entries: %v", entries)
	}
}

func TestExtractBundleToStagingValidatesAndNormalizesModes(t *testing.T) {
	const id = "com.example.chatdump"
	executable := "server/dist/plugin-" + runtime.GOOS + "-" + runtime.GOARCH
	webBundle := "webapp/dist/main.js"
	bundle := makeBundle(t,
		dirEntry(id+"/"),
		fileEntry(id+"/plugin.json", manifestJSON(t, id, executable, webBundle)),
		dirEntry(id+"/server/"),
		dirEntry(id+"/server/dist/"),
		fileEntry(id+"/"+executable, []byte("server binary")),
		dirEntry(id+"/webapp/"),
		dirEntry(id+"/webapp/dist/"),
		fileEntry(id+"/"+webBundle, []byte("web bundle")),
		fileEntry(id+"/asset.txt", []byte("asset")),
	)

	parent := t.TempDir()
	staged, err := ExtractBundleToStaging(context.Background(), parent, bytes.NewReader(bundle))
	if err != nil {
		t.Fatalf("ExtractBundleToStaging() returned error: %v", err)
	}
	if staged.Manifest.ID != id {
		t.Fatalf("manifest id = %q, want %q", staged.Manifest.ID, id)
	}
	if staged.PluginDir != filepath.Join(staged.StagingDir, id) {
		t.Fatalf("plugin dir = %q, want staging root joined with id", staged.PluginDir)
	}
	if rel, err := filepath.Rel(parent, staged.StagingDir); err != nil || rel == ".." || filepath.IsAbs(rel) {
		t.Fatalf("staging directory %q is not below %q", staged.StagingDir, parent)
	}

	assertMode := func(relative string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(filepath.Join(staged.PluginDir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %o, want %o", relative, got, want)
		}
	}
	assertMode("plugin.json", 0o644)
	assertMode(executable, 0o755)
	assertMode(webBundle, 0o644)
	assertMode("asset.txt", 0o644)

	stagingDir := staged.StagingDir
	if err := staged.Cleanup(); err != nil {
		t.Fatalf("Cleanup() returned error: %v", err)
	}
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging directory still exists after cleanup: %v", err)
	}
}

func TestValidateArchivePathRejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: "", want: "empty"},
		{name: "absolute", path: "/plugin/plugin.json", want: "absolute"},
		{name: "windows absolute", path: "C:/plugin/plugin.json", want: "absolute"},
		{name: "backslash", path: `plugin\plugin.json`, want: "backslash"},
		{name: "nul", path: "plugin/evil\x00name", want: "NUL"},
		{name: "parent", path: "../plugin/plugin.json", want: "traversal"},
		{name: "embedded parent", path: "plugin/../../escape", want: "traversal"},
		{name: "dot component", path: "plugin/./plugin.json", want: "non-canonical"},
		{name: "double slash", path: "plugin//plugin.json", want: "non-canonical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := validateArchivePath(tt.path, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateArchivePath(%q) error = %v, want %q", tt.path, err, tt.want)
			}
		})
	}
}

func TestExtractBundleRejectsUnsafeArchiveEntries(t *testing.T) {
	const root = "com.example.plugin"
	baseManifest := fileEntry(root+"/plugin.json", manifestJSON(t, root, "", "web/main.js"))

	tests := []struct {
		name  string
		entry bundleEntry
		want  string
	}{
		{name: "traversal", entry: fileEntry(root+"/../../escape", []byte("bad")), want: "traversal"},
		{name: "absolute", entry: fileEntry("/absolute", []byte("bad")), want: "absolute"},
		{name: "backslash", entry: fileEntry(root+`\escape`, []byte("bad")), want: "backslash"},
		{name: "symlink", entry: bundleEntry{header: tar.Header{Name: root + "/link", Typeflag: tar.TypeSymlink, Linkname: "target"}}, want: "unsupported type"},
		{name: "hardlink", entry: bundleEntry{header: tar.Header{Name: root + "/link", Typeflag: tar.TypeLink, Linkname: root + "/target"}}, want: "unsupported type"},
		{name: "character device", entry: bundleEntry{header: tar.Header{Name: root + "/device", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}}, want: "unsupported type"},
		{name: "block device", entry: bundleEntry{header: tar.Header{Name: root + "/device", Typeflag: tar.TypeBlock, Devmajor: 1, Devminor: 0}}, want: "unsupported type"},
		{name: "fifo", entry: bundleEntry{header: tar.Header{Name: root + "/fifo", Typeflag: tar.TypeFifo}}, want: "unsupported type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle := makeBundle(t, dirEntry(root+"/"), tt.entry, baseManifest)
			expectStageError(t, bundle, DefaultBundleLimits(), tt.want)
		})
	}
}

func TestExtractBundleRejectsDuplicatesAndMultipleRoots(t *testing.T) {
	const root = "com.example.plugin"
	manifest := manifestJSON(t, root, "", "web/main.js")

	duplicate := makeBundle(t,
		dirEntry(root+"/"),
		fileEntry(root+"/plugin.json", manifest),
		fileEntry(root+"/plugin.json", manifest),
	)
	expectStageError(t, duplicate, DefaultBundleLimits(), "duplicate entry")

	multipleRoots := makeBundle(t,
		dirEntry(root+"/"),
		fileEntry(root+"/plugin.json", manifest),
		fileEntry("other/file.txt", []byte("bad")),
	)
	expectStageError(t, multipleRoots, DefaultBundleLimits(), "multiple roots")

	multipleManifests := makeBundle(t,
		dirEntry(root+"/"),
		fileEntry(root+"/plugin.json", manifest),
		fileEntry(root+"/plugin.yaml", []byte("id: duplicate")),
	)
	expectStageError(t, multipleManifests, DefaultBundleLimits(), "multiple root manifests")
}

func TestExtractBundleRequiresRootToMatchManifestID(t *testing.T) {
	bundle := makeBundle(t,
		dirEntry("archive-root/"),
		fileEntry("archive-root/plugin.json", manifestJSON(t, "different.id", "", "web/main.js")),
		fileEntry("archive-root/web/main.js", []byte("web")),
	)
	expectStageError(t, bundle, DefaultBundleLimits(), "does not match manifest id")
}

func TestExtractBundleValidatesManifestEntrypoints(t *testing.T) {
	const root = "com.example.plugin"
	tests := []struct {
		name     string
		manifest []byte
		extra    []bundleEntry
		want     string
	}{
		{
			name:     "malformed manifest",
			manifest: []byte("{"),
			want:     "invalid manifest",
		},
		{
			name:     "missing current executable",
			manifest: manifestJSON(t, root, "server/missing", ""),
			want:     "inspect server executable",
		},
		{
			name:     "executable is directory",
			manifest: manifestJSON(t, root, "server/run", ""),
			extra:    []bundleEntry{dirEntry(root + "/server/run/")},
			want:     "is not a regular file",
		},
		{
			name:     "executable traversal",
			manifest: manifestJSON(t, root, "../outside", ""),
			want:     "invalid server executable path",
		},
		{
			name:     "missing web bundle",
			manifest: manifestJSON(t, root, "", "web/missing.js"),
			want:     "inspect webapp bundle",
		},
		{
			name:     "web bundle is directory",
			manifest: manifestJSON(t, root, "", "web/main.js"),
			extra:    []bundleEntry{dirEntry(root + "/web/main.js/")},
			want:     "is not a regular file",
		},
		{
			name:     "web bundle absolute",
			manifest: manifestJSON(t, root, "", "/web/main.js"),
			want:     "invalid webapp bundle path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []bundleEntry{
				dirEntry(root + "/"),
				fileEntry(root+"/plugin.json", tt.manifest),
			}
			entries = append(entries, tt.extra...)
			expectStageError(t, makeBundle(t, entries...), DefaultBundleLimits(), tt.want)
		})
	}
}

func TestExtractBundleEnforcesResourceLimits(t *testing.T) {
	const root = "com.example.plugin"
	manifest := manifestJSON(t, root, "", "web/main.js")
	validEntries := []bundleEntry{
		dirEntry(root + "/"),
		fileEntry(root+"/plugin.json", manifest),
		fileEntry(root+"/web/main.js", []byte("web")),
	}
	bundle := makeBundle(t, validEntries...)

	entryLimits := DefaultBundleLimits()
	entryLimits.MaxEntries = 2
	expectStageError(t, bundle, entryLimits, "entry count exceeds")

	fileLimits := DefaultBundleLimits()
	fileLimits.MaxFileBytes = int64(len(manifest) - 1)
	expectStageError(t, bundle, fileLimits, "exceeds")

	expandedLimits := DefaultBundleLimits()
	expandedLimits.MaxExpandedBytes = 1024
	expectStageError(t, bundle, expandedLimits, "expanded data exceeds")

	compressedLimits := DefaultBundleLimits()
	compressedLimits.MaxBundleBytes = 16
	expectStageError(t, bundle, compressedLimits, "compressed data exceeds")
}

func TestExtractBundleRejectsInvalidLimitsAndCanceledContext(t *testing.T) {
	parent := t.TempDir()
	_, err := ExtractBundleToStagingWithLimits(context.Background(), parent, bytes.NewReader(nil), BundleLimits{})
	if err == nil || !strings.Contains(err.Error(), "limits") {
		t.Fatalf("invalid limits error = %v", err)
	}

	const root = "com.example.plugin"
	bundle := makeBundle(t,
		dirEntry(root+"/"),
		fileEntry(root+"/plugin.json", manifestJSON(t, root, "", "web/main.js")),
		fileEntry(root+"/web/main.js", []byte("web")),
	)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ExtractBundleToStaging(canceled, parent, bytes.NewReader(bundle))
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled extraction error = %v", err)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled extraction left staging entries: %v", entries)
	}
}

func TestExecutableForPlatformFallsBackToDefault(t *testing.T) {
	manifest := &Manifest{Server: &ServerSpec{Executable: "server/default"}}
	if got := executableForPlatform(manifest, runtime.GOOS, runtime.GOARCH); got != "server/default" {
		t.Fatalf("executableForPlatform() = %q", got)
	}
	manifest.Server.Executables = map[string]string{runtime.GOOS + "-" + runtime.GOARCH: "server/platform"}
	if got := executableForPlatform(manifest, runtime.GOOS, runtime.GOARCH); got != "server/platform" {
		t.Fatalf("executableForPlatform() = %q", got)
	}
}

func TestArchiveErrorIncludesEntryName(t *testing.T) {
	const root = "com.example.plugin"
	bundle := makeBundle(t,
		dirEntry(root+"/"),
		bundleEntry{header: tar.Header{Name: root + "/socket", Typeflag: tar.TypeFifo}},
	)
	parent := t.TempDir()
	_, err := ExtractBundleToStaging(context.Background(), parent, bytes.NewReader(bundle))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", root+"/socket")) {
		t.Fatalf("error = %v, want rejected entry name", err)
	}
}
