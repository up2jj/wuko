package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const MarkerName = ".wuko-plugin.json"

type InstallationMarker struct {
	ManifestVersion int    `json:"manifest_version"`
	Namespace       string `json:"namespace"`
	PluginVersion   string `json:"plugin_version"`
	Protocol        string `json:"protocol,omitempty"`
	Source          string `json:"source"`
	ManifestDigest  string `json:"manifest_digest"`
	ArtifactDigest  string `json:"artifact_digest"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
}

func Install(ctx context.Context, source, destinationRoot string, reinstall bool, clientHTTP *http.Client, stderr io.Writer) (InstallationMarker, error) {
	return InstallPinned(ctx, source, "", destinationRoot, reinstall, clientHTTP, stderr)
}

// InstallPinned installs a plugin and verifies the release manifest against expectedManifestDigest.
func InstallPinned(ctx context.Context, source, expectedManifestDigest, destinationRoot string, reinstall bool, clientHTTP *http.Client, stderr io.Writer) (InstallationMarker, error) {
	release, err := FetchRelease(ctx, source, expectedManifestDigest, clientHTTP)
	if err != nil {
		return InstallationMarker{}, err
	}
	return installRelease(ctx, release, destinationRoot, reinstall, stderr)
}

// InstallMarketplace installs a catalog-pinned release after checking its catalog identity.
func InstallMarketplace(ctx context.Context, source, expectedManifestDigest, expectedNamespace, expectedVersion, destinationRoot string, reinstall bool, clientHTTP *http.Client, stderr io.Writer) (InstallationMarker, error) {
	release, err := FetchRelease(ctx, source, expectedManifestDigest, clientHTTP)
	if err != nil {
		return InstallationMarker{}, err
	}
	if release.Manifest.Namespace != expectedNamespace {
		return InstallationMarker{}, fmt.Errorf("marketplace namespace %q conflicts with plugin manifest namespace %q", expectedNamespace, release.Manifest.Namespace)
	}
	if release.Manifest.PluginVersion != expectedVersion {
		return InstallationMarker{}, fmt.Errorf("marketplace version %q conflicts with plugin manifest version %q", expectedVersion, release.Manifest.PluginVersion)
	}
	return installRelease(ctx, release, destinationRoot, reinstall, stderr)
}

func installRelease(ctx context.Context, release Release, destinationRoot string, reinstall bool, stderr io.Writer) (InstallationMarker, error) {
	destination := filepath.Join(destinationRoot, release.Manifest.Namespace)
	if _, err := os.Stat(destination); err == nil && !reinstall {
		return InstallationMarker{}, fmt.Errorf("plugin %q is already installed; use --reinstall", release.Manifest.Namespace)
	} else if err != nil && !os.IsNotExist(err) {
		return InstallationMarker{}, err
	}
	if err := os.MkdirAll(destinationRoot, 0700); err != nil {
		return InstallationMarker{}, err
	}
	stage, err := os.MkdirTemp(destinationRoot, ".plugin-stage-")
	if err != nil {
		return InstallationMarker{}, err
	}
	defer os.RemoveAll(stage)
	executable, err := Extract(release, stage)
	if err != nil {
		return InstallationMarker{}, err
	}
	if err := verifyExecutable(ctx, executable, release.Manifest.Namespace, release.Manifest.Protocol, stderr); err != nil {
		return InstallationMarker{}, err
	}
	marker := InstallationMarker{ManifestVersion: release.Manifest.Version, Namespace: release.Manifest.Namespace, PluginVersion: release.Manifest.PluginVersion, Protocol: release.Manifest.Protocol, Source: release.CanonicalSource, ManifestDigest: release.ManifestDigest, ArtifactDigest: release.Artifact.SHA256, OS: runtime.GOOS, Arch: runtime.GOARCH}
	// A v1 installation leaves protocol out of the file so a marker written here stays readable
	// by an older wuko, which decodes markers with unknown fields disallowed and would otherwise
	// reject every installation -- list and uninstall included -- after a downgrade.
	// ValidateInstallation reads an absent field back as v1. A v2 marker does record it, and an
	// older binary refusing a plugin it could not have run is the outcome that belongs there.
	stored := marker
	if stored.Protocol == ProtocolV1 {
		stored.Protocol = ""
	}
	data, _ := json.MarshalIndent(stored, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(stage, MarkerName), data, 0600); err != nil {
		return InstallationMarker{}, err
	}
	backup := ""
	if reinstall {
		if _, err := os.Stat(destination); err == nil {
			backupDirectory, err := os.MkdirTemp(destinationRoot, ".plugin-backup-")
			if err != nil {
				return InstallationMarker{}, err
			}
			if err := os.Remove(backupDirectory); err != nil {
				return InstallationMarker{}, err
			}
			backup = backupDirectory
			if err := os.Rename(destination, backup); err != nil {
				return InstallationMarker{}, err
			}
		}
	}
	if err := os.Rename(stage, destination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return InstallationMarker{}, err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return marker, nil
}

func verifyExecutable(ctx context.Context, path, namespace, protocol string, stderr io.Writer) error {
	client, err := launch(ctx, path, stderr)
	if err != nil {
		return err
	}
	var initialized initializeResult
	callErr := client.call(ctx, "initialize", map[string]any{"protocol": protocol}, &initialized, nil)
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeErr := client.close(closeCtx)
	if callErr != nil {
		return callErr
	}
	if closeErr != nil {
		return closeErr
	}
	if initialized.Protocol != protocol || initialized.Namespace != namespace {
		return fmt.Errorf("plugin handshake namespace or protocol mismatch")
	}
	return validateInitializeDeclarations(namespace, protocol, initialized)
}

func ValidateInstallation(directory, namespace string) (InstallationMarker, error) {
	data, err := os.ReadFile(filepath.Join(directory, MarkerName))
	if err != nil {
		return InstallationMarker{}, fmt.Errorf("reading plugin marker: %w", err)
	}
	var marker InstallationMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return InstallationMarker{}, fmt.Errorf("invalid plugin marker: %w", err)
	}
	if marker.Protocol == "" {
		marker.Protocol = ProtocolV1
	}
	if marker.ManifestVersion != 1 || marker.Namespace != namespace || marker.PluginVersion == "" || !supportedProtocol(marker.Protocol) || marker.Source == "" || marker.OS == "" || marker.Arch == "" || !validDigest(marker.ManifestDigest) || !validDigest(marker.ArtifactDigest) {
		return InstallationMarker{}, fmt.Errorf("invalid plugin marker")
	}
	expected := filepath.Join(directory, "wuko-plugin-"+namespace)
	info, err := os.Stat(expected)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return InstallationMarker{}, fmt.Errorf("invalid plugin installation")
	}
	return marker, nil
}
