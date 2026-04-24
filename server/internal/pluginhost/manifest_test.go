package pluginhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifestFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestLoadManifestJSON(t *testing.T) {
	dir := t.TempDir()
	writeManifestFile(t, dir, "plugin.json", `{
		"id": "acme.echo",
		"name": "Echo",
		"version": "1.0.0",
		"server": {
			"executable": "server/echo"
		},
		"hooks": ["message.created"]
	}`)

	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest() returned error: %v", err)
	}

	if manifest.ID != "acme.echo" {
		t.Fatalf("ID = %q, want acme.echo", manifest.ID)
	}
	if manifest.Server == nil || manifest.Server.Executable != "server/echo" {
		t.Fatalf("Server = %#v, want executable server/echo", manifest.Server)
	}
	if manifest.Name != "Echo" {
		t.Fatalf("Name = %q, want Echo", manifest.Name)
	}
}

func TestLoadManifestYAML(t *testing.T) {
	dir := t.TempDir()
	writeManifestFile(t, dir, "plugin.yaml", `
id: acme.web
name: Web Plugin
version: 2.1.0
webapp:
  bundle_path: web/index.js
`)

	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest() returned error: %v", err)
	}

	if manifest.ID != "acme.web" {
		t.Fatalf("ID = %q, want acme.web", manifest.ID)
	}
	if manifest.Webapp == nil || manifest.Webapp.BundlePath != "web/index.js" {
		t.Fatalf("Webapp = %#v, want bundle web/index.js", manifest.Webapp)
	}
}

func TestLoadManifestValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing id",
			body: `{"version":"1.0.0","server":{"executable":"server/run"}}`,
			want: "manifest.id required",
		},
		{
			name: "missing version",
			body: `{"id":"acme.echo","server":{"executable":"server/run"}}`,
			want: "manifest.version required",
		},
		{
			name: "missing entrypoint",
			body: `{"id":"acme.echo","version":"1.0.0"}`,
			want: "must define server or webapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifestFile(t, dir, "plugin.json", tt.body)

			_, err := LoadManifest(dir)
			if err == nil {
				t.Fatalf("LoadManifest() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadManifest() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestExecutablePath(t *testing.T) {
	dir := filepath.Join("plugins", "acme")
	manifest := &Manifest{
		Server: &ServerSpec{
			Executable: "server/default",
			Executables: map[string]string{
				"windows-amd64": "server/windows.exe",
			},
		},
	}

	got := manifest.ExecutablePath(dir, "windows", "amd64")
	want := filepath.Join(dir, "server", "windows.exe")
	if got != want {
		t.Fatalf("ExecutablePath(windows-amd64) = %q, want %q", got, want)
	}

	got = manifest.ExecutablePath(dir, "linux", "amd64")
	want = filepath.Join(dir, "server", "default")
	if got != want {
		t.Fatalf("ExecutablePath(linux-amd64) = %q, want %q", got, want)
	}
}
