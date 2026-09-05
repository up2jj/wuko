// Package plugin implements executable Wuko plugins and their release format.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
)

const Protocol = "wuko.plugin/v1"

type Manifest struct {
	Version       int        `json:"version"`
	Namespace     string     `json:"namespace"`
	PluginVersion string     `json:"plugin_version"`
	Protocol      string     `json:"protocol"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Format string `json:"format"`
	Entry  string `json:"entry"`
	SHA256 string `json:"sha256"`
}

func ParseManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decoding plugin manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, fmt.Errorf("plugin manifest contains trailing data")
	}
	if manifest.Version != 1 || manifest.Protocol != Protocol {
		return Manifest{}, fmt.Errorf("unsupported plugin manifest version or protocol")
	}
	if !validNamespace(manifest.Namespace) || strings.TrimSpace(manifest.PluginVersion) == "" {
		return Manifest{}, fmt.Errorf("plugin manifest has an invalid namespace or version")
	}
	seen := make(map[string]bool)
	for _, artifact := range manifest.Artifacts {
		key := artifact.OS + "/" + artifact.Arch
		if seen[key] {
			return Manifest{}, fmt.Errorf("duplicate plugin artifact for %s", key)
		}
		seen[key] = true
		if (artifact.OS != "darwin" && artifact.OS != "linux") || (artifact.Arch != "amd64" && artifact.Arch != "arm64") || artifact.Format != "tar.gz" || artifact.Entry != "wuko-plugin-"+manifest.Namespace || !validDigest(artifact.SHA256) {
			return Manifest{}, fmt.Errorf("invalid plugin artifact for %s", key)
		}
		if _, err := safeRelative(artifact.Path); err != nil {
			return Manifest{}, fmt.Errorf("invalid plugin artifact for %s: %w", key, err)
		}
	}
	return manifest, nil
}

func (manifest Manifest) CurrentArtifact() (Artifact, error) {
	for _, artifact := range manifest.Artifacts {
		if artifact.OS == runtime.GOOS && artifact.Arch == runtime.GOARCH {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("plugin %q has no artifact for %s/%s", manifest.Namespace, runtime.GOOS, runtime.GOARCH)
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validNamespace(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
