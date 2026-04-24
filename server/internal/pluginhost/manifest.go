package pluginhost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is Mattermost-compatible. Unknown fields are preserved in Extra.
type Manifest struct {
	ID               string          `json:"id" yaml:"id"`
	Name             string          `json:"name" yaml:"name"`
	Description      string          `json:"description,omitempty" yaml:"description,omitempty"`
	Version          string          `json:"version" yaml:"version"`
	MinServerVersion string          `json:"min_server_version,omitempty" yaml:"min_server_version,omitempty"`
	HomepageURL      string          `json:"homepage_url,omitempty" yaml:"homepage_url,omitempty"`
	Server           *ServerSpec     `json:"server,omitempty" yaml:"server,omitempty"`
	Webapp           *WebappSpec     `json:"webapp,omitempty" yaml:"webapp,omitempty"`
	SettingsSchema   json.RawMessage `json:"settings_schema,omitempty" yaml:"settings_schema,omitempty"`
}

type ServerSpec struct {
	Executable  string            `json:"executable,omitempty" yaml:"executable,omitempty"`
	Executables map[string]string `json:"executables,omitempty" yaml:"executables,omitempty"`
}

type WebappSpec struct {
	BundlePath string `json:"bundle_path,omitempty" yaml:"bundle_path,omitempty"`
}

// LoadManifest reads plugin.json or plugin.yaml from a plugin directory.
func LoadManifest(dir string) (*Manifest, error) {
	candidates := []string{"plugin.json", "plugin.yaml", "plugin.yml"}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		m := &Manifest{}
		if strings.HasSuffix(name, ".json") {
			if err := json.Unmarshal(data, m); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		} else {
			if err := yaml.Unmarshal(data, m); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("no plugin manifest in %s", dir)
}

func (m *Manifest) validate() error {
	if m.ID == "" {
		return fmt.Errorf("manifest.id required")
	}
	if m.Version == "" {
		return fmt.Errorf("manifest.version required")
	}
	if m.Server == nil && m.Webapp == nil {
		return fmt.Errorf("manifest must define server or webapp")
	}
	return nil
}

// ExecutablePath resolves the server executable for the current platform.
func (m *Manifest) ExecutablePath(dir, goos, goarch string) string {
	if m.Server == nil {
		return ""
	}
	if m.Server.Executables != nil {
		key := goos + "-" + goarch
		if p, ok := m.Server.Executables[key]; ok {
			return filepath.Join(dir, p)
		}
	}
	if m.Server.Executable != "" {
		return filepath.Join(dir, m.Server.Executable)
	}
	return ""
}
