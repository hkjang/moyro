package pluginhost

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultMaxBundleBytes   int64 = 256 << 20
	defaultMaxExpandedBytes int64 = 512 << 20
	defaultMaxFileBytes     int64 = 128 << 20
	defaultMaxEntries             = 4096
)

// BundleLimits bounds the resources consumed while validating and extracting
// a plugin archive. MaxExpandedBytes covers the complete uncompressed tar
// stream, including tar headers and padding, not just regular-file contents.
type BundleLimits struct {
	MaxBundleBytes   int64
	MaxExpandedBytes int64
	MaxFileBytes     int64
	MaxEntries       int
}

// DefaultBundleLimits accepts ordinary multi-platform Mattermost plugin
// bundles while keeping archive parsing and extraction bounded.
func DefaultBundleLimits() BundleLimits {
	return BundleLimits{
		MaxBundleBytes:   defaultMaxBundleBytes,
		MaxExpandedBytes: defaultMaxExpandedBytes,
		MaxFileBytes:     defaultMaxFileBytes,
		MaxEntries:       defaultMaxEntries,
	}
}

// StagedPlugin is a validated plugin extracted into a private temporary
// directory. Callers may atomically move PluginDir into the live plugin
// directory. StagingDir remains the cleanup boundary if installation fails.
type StagedPlugin struct {
	StagingDir string
	PluginDir  string
	Manifest   *Manifest
}

// Cleanup removes the complete staging directory.
func (s *StagedPlugin) Cleanup() error {
	if s == nil || s.StagingDir == "" {
		return nil
	}
	return os.RemoveAll(s.StagingDir)
}

// ExtractBundleToStaging validates and extracts a gzip-compressed tar plugin
// bundle below stagingParent. It never writes outside the newly-created
// staging directory and removes partial output on failure.
func ExtractBundleToStaging(ctx context.Context, stagingParent string, archive io.Reader) (*StagedPlugin, error) {
	return ExtractBundleToStagingWithLimits(ctx, stagingParent, archive, DefaultBundleLimits())
}

// ExtractBundleToStagingWithLimits is ExtractBundleToStaging with explicit
// resource limits. It is useful to tighten policy at a deployment boundary.
func ExtractBundleToStagingWithLimits(ctx context.Context, stagingParent string, archive io.Reader, limits BundleLimits) (_ *StagedPlugin, retErr error) {
	if ctx == nil {
		return nil, errors.New("plugin bundle: nil context")
	}
	if archive == nil {
		return nil, errors.New("plugin bundle: nil archive")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}

	stagingDir, err := os.MkdirTemp(stagingParent, ".moyro-plugin-stage-")
	if err != nil {
		return nil, fmt.Errorf("plugin bundle: create staging directory: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	compressed := &io.LimitedReader{R: archive, N: limits.MaxBundleBytes + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		if compressed.N == 0 {
			return nil, fmt.Errorf("plugin bundle: compressed data exceeds %d bytes", limits.MaxBundleBytes)
		}
		return nil, fmt.Errorf("plugin bundle: open gzip stream: %w", err)
	}
	defer gz.Close()

	expanded := &io.LimitedReader{R: gz, N: limits.MaxExpandedBytes + 1}
	tr := tar.NewReader(expanded)
	seen := make(map[string]struct{})
	rootName := ""
	entryCount := 0
	manifestCount := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plugin bundle: extraction canceled: %w", err)
		}

		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if expanded.N == 0 {
				return nil, fmt.Errorf("plugin bundle: expanded data exceeds %d bytes", limits.MaxExpandedBytes)
			}
			if compressed.N == 0 {
				return nil, fmt.Errorf("plugin bundle: compressed data exceeds %d bytes", limits.MaxBundleBytes)
			}
			return nil, fmt.Errorf("plugin bundle: read tar header: %w", err)
		}

		entryCount++
		if entryCount > limits.MaxEntries {
			return nil, fmt.Errorf("plugin bundle: entry count exceeds %d", limits.MaxEntries)
		}

		cleanName, entryRoot, err := validateArchivePath(hdr.Name, hdr.Typeflag == tar.TypeDir)
		if err != nil {
			return nil, fmt.Errorf("plugin bundle: entry %q: %w", hdr.Name, err)
		}
		if rootName == "" {
			rootName = entryRoot
		} else if entryRoot != rootName {
			return nil, fmt.Errorf("plugin bundle: multiple roots %q and %q", rootName, entryRoot)
		}
		if cleanName == rootName && hdr.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("plugin bundle: root %q must be a directory", rootName)
		}

		duplicateKey := cleanName
		if runtime.GOOS == "windows" {
			duplicateKey = strings.ToLower(duplicateKey)
		}
		if _, ok := seen[duplicateKey]; ok {
			return nil, fmt.Errorf("plugin bundle: duplicate entry %q", cleanName)
		}
		seen[duplicateKey] = struct{}{}

		if strings.ContainsRune(hdr.Linkname, '\x00') {
			return nil, fmt.Errorf("plugin bundle: entry %q has NUL in link target", cleanName)
		}
		if hdr.Linkname != "" && strings.Contains(hdr.Linkname, `\`) {
			return nil, fmt.Errorf("plugin bundle: entry %q has backslash in link target", cleanName)
		}
		if hdr.Size < 0 {
			return nil, fmt.Errorf("plugin bundle: entry %q has negative size", cleanName)
		}
		if hdr.Size > limits.MaxFileBytes {
			return nil, fmt.Errorf("plugin bundle: entry %q exceeds %d bytes", cleanName, limits.MaxFileBytes)
		}

		target, err := secureJoin(stagingDir, cleanName)
		if err != nil {
			return nil, fmt.Errorf("plugin bundle: entry %q: %w", cleanName, err)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if hdr.Size != 0 {
				return nil, fmt.Errorf("plugin bundle: directory %q has non-zero size", cleanName)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, fmt.Errorf("plugin bundle: create directory %q: %w", cleanName, err)
			}
			if err := os.Chmod(target, 0o755); err != nil {
				return nil, fmt.Errorf("plugin bundle: set directory mode %q: %w", cleanName, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, fmt.Errorf("plugin bundle: create parent for %q: %w", cleanName, err)
			}
			if err := writeArchiveFile(ctx, target, tr, hdr.Size); err != nil {
				if expanded.N == 0 {
					return nil, fmt.Errorf("plugin bundle: expanded data exceeds %d bytes", limits.MaxExpandedBytes)
				}
				if compressed.N == 0 {
					return nil, fmt.Errorf("plugin bundle: compressed data exceeds %d bytes", limits.MaxBundleBytes)
				}
				return nil, fmt.Errorf("plugin bundle: extract %q: %w", cleanName, err)
			}
			if err := os.Chmod(target, 0o644); err != nil {
				return nil, fmt.Errorf("plugin bundle: set file mode %q: %w", cleanName, err)
			}
			if path.Dir(cleanName) == rootName && isManifestName(path.Base(cleanName)) {
				manifestCount++
				if manifestCount > 1 {
					return nil, errors.New("plugin bundle: multiple root manifests")
				}
			}

		default:
			return nil, fmt.Errorf("plugin bundle: entry %q has unsupported type %d", cleanName, hdr.Typeflag)
		}
	}

	if rootName == "" {
		return nil, errors.New("plugin bundle: archive is empty")
	}
	if manifestCount != 1 {
		return nil, errors.New("plugin bundle: exactly one root manifest is required")
	}

	if _, err := io.Copy(io.Discard, expanded); err != nil {
		if compressed.N == 0 {
			return nil, fmt.Errorf("plugin bundle: compressed data exceeds %d bytes", limits.MaxBundleBytes)
		}
		return nil, fmt.Errorf("plugin bundle: finish gzip stream: %w", err)
	}
	if expanded.N == 0 {
		return nil, fmt.Errorf("plugin bundle: expanded data exceeds %d bytes", limits.MaxExpandedBytes)
	}
	if compressed.N == 0 {
		return nil, fmt.Errorf("plugin bundle: compressed data exceeds %d bytes", limits.MaxBundleBytes)
	}

	pluginDir := filepath.Join(stagingDir, filepath.FromSlash(rootName))
	if err := os.Chmod(pluginDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugin bundle: set plugin root mode: %w", err)
	}
	manifest, err := LoadManifest(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("plugin bundle: invalid manifest: %w", err)
	}
	if manifest.ID != rootName {
		return nil, fmt.Errorf("plugin bundle: root %q does not match manifest id %q", rootName, manifest.ID)
	}

	manifestPath, err := rootManifestPath(pluginDir)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		return nil, fmt.Errorf("plugin bundle: set manifest mode: %w", err)
	}

	var webPath string
	if manifest.Webapp != nil {
		webPath, err = validateReferencedFile(pluginDir, manifest.Webapp.BundlePath, "webapp bundle")
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(webPath, 0o644); err != nil {
			return nil, fmt.Errorf("plugin bundle: set webapp bundle mode: %w", err)
		}
	}

	if manifest.Server != nil {
		executableRel := executableForPlatform(manifest, runtime.GOOS, runtime.GOARCH)
		executablePath, err := validateReferencedFile(pluginDir, executableRel, "server executable")
		if err != nil {
			return nil, err
		}
		if executablePath == manifestPath || executablePath == webPath {
			return nil, errors.New("plugin bundle: server executable overlaps manifest or webapp bundle")
		}
		if err := os.Chmod(executablePath, 0o755); err != nil {
			return nil, fmt.Errorf("plugin bundle: set server executable mode: %w", err)
		}
	}
	// Installation commits with directory renames. Sync every extracted file
	// and directory first so a power loss cannot leave a committed name whose
	// executable or manifest data was never made durable.
	if err := syncPluginTree(pluginDir); err != nil {
		return nil, fmt.Errorf("plugin bundle: sync extracted tree: %w", err)
	}

	return &StagedPlugin{
		StagingDir: stagingDir,
		PluginDir:  pluginDir,
		Manifest:   manifest,
	}, nil
}

func (l BundleLimits) validate() error {
	if l.MaxBundleBytes < 1 || l.MaxExpandedBytes < 1 || l.MaxFileBytes < 1 || l.MaxEntries < 1 {
		return errors.New("plugin bundle: all extraction limits must be positive")
	}
	if l.MaxBundleBytes == int64(^uint64(0)>>1) || l.MaxExpandedBytes == int64(^uint64(0)>>1) {
		return errors.New("plugin bundle: byte limits are too large")
	}
	return nil
}

func validateArchivePath(name string, directory bool) (cleanName string, root string, err error) {
	if name == "" {
		return "", "", errors.New("empty path")
	}
	if strings.ContainsRune(name, '\x00') {
		return "", "", errors.New("NUL in path")
	}
	if strings.Contains(name, `\`) {
		return "", "", errors.New("backslash in path")
	}
	if path.IsAbs(name) || isWindowsDrivePath(name) {
		return "", "", errors.New("absolute path")
	}

	trimmed := name
	if directory {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	cleanName = path.Clean(trimmed)
	if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", "", errors.New("path traversal")
	}
	if cleanName != trimmed {
		return "", "", errors.New("non-canonical path")
	}

	root, _, _ = strings.Cut(cleanName, "/")
	if root == "" || root == "." || root == ".." {
		return "", "", errors.New("invalid root")
	}
	return cleanName, root, nil
}

func isWindowsDrivePath(name string) bool {
	return len(name) >= 2 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':'
}

func secureJoin(base, archivePath string) (string, error) {
	target := filepath.Join(base, filepath.FromSlash(archivePath))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes staging directory")
	}
	return target, nil
}

func writeArchiveFile(ctx context.Context, target string, src io.Reader, size int64) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	remaining := size
	buffer := make([]byte, 32<<10)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return err
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		n, readErr := io.ReadFull(src, buffer[:chunk])
		if n > 0 {
			if _, err := f.Write(buffer[:n]); err != nil {
				_ = f.Close()
				return err
			}
			remaining -= int64(n)
		}
		if readErr != nil {
			_ = f.Close()
			return readErr
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncPluginTree(root string) error {
	directories := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular extracted path %q", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

func isManifestName(name string) bool {
	switch name {
	case "plugin.json", "plugin.yaml", "plugin.yml":
		return true
	default:
		return false
	}
}

func rootManifestPath(pluginDir string) (string, error) {
	for _, name := range []string{"plugin.json", "plugin.yaml", "plugin.yml"} {
		candidate := filepath.Join(pluginDir, name)
		info, err := os.Lstat(candidate)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("plugin bundle: manifest %q is not a regular file", name)
			}
			return candidate, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("plugin bundle: inspect manifest %q: %w", name, err)
		}
	}
	return "", errors.New("plugin bundle: root manifest not found")
}

func executableForPlatform(manifest *Manifest, goos, goarch string) string {
	if manifest == nil || manifest.Server == nil {
		return ""
	}
	if executable, ok := manifest.Server.Executables[goos+"-"+goarch]; ok {
		return executable
	}
	return manifest.Server.Executable
}

func validateReferencedFile(pluginDir, relative, label string) (string, error) {
	if relative == "" {
		return "", fmt.Errorf("plugin bundle: %s is not defined for %s-%s", label, runtime.GOOS, runtime.GOARCH)
	}
	cleanName, _, err := validateArchivePath(relative, false)
	if err != nil {
		return "", fmt.Errorf("plugin bundle: invalid %s path %q: %w", label, relative, err)
	}
	target, err := secureJoin(pluginDir, cleanName)
	if err != nil {
		return "", fmt.Errorf("plugin bundle: invalid %s path %q: %w", label, relative, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("plugin bundle: inspect %s %q: %w", label, relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin bundle: %s %q is not a regular file", label, relative)
	}
	return target, nil
}
