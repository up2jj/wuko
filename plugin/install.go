package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const MarkerName = ".wuko-plugin.json"

const currentMarkerVersion = 2

type InstallationMarker struct {
	MarkerVersion    int    `json:"marker_version,omitempty"`
	ManifestVersion  int    `json:"manifest_version"`
	Namespace        string `json:"namespace"`
	PluginVersion    string `json:"plugin_version"`
	Protocol         string `json:"protocol,omitempty"`
	Source           string `json:"source"`
	ManifestDigest   string `json:"manifest_digest"`
	ArtifactDigest   string `json:"artifact_digest"`
	ExecutableDigest string `json:"executable_digest,omitempty"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
}

// MarketplaceInstallRequest pins one plugin selected from a marketplace.
type MarketplaceInstallRequest struct {
	Source         string
	ManifestDigest string
	Namespace      string
	PluginVersion  string
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
	markers, err := installReleases(ctx, []Release{release}, destinationRoot, reinstall, stderr)
	if len(markers) == 0 {
		return InstallationMarker{}, err
	}
	return markers[0], err
}

// InstallMarketplace installs a catalog-pinned release after checking its catalog identity.
func InstallMarketplace(ctx context.Context, source, expectedManifestDigest, expectedNamespace, expectedVersion, destinationRoot string, reinstall bool, clientHTTP *http.Client, stderr io.Writer) (InstallationMarker, error) {
	request := MarketplaceInstallRequest{
		Source:         source,
		ManifestDigest: expectedManifestDigest,
		Namespace:      expectedNamespace,
		PluginVersion:  expectedVersion,
	}
	markers, err := InstallMarketplaceBatch(ctx, []MarketplaceInstallRequest{request}, destinationRoot, reinstall, clientHTTP, stderr)
	if len(markers) == 0 {
		return InstallationMarker{}, err
	}
	return markers[0], err
}

// InstallMarketplaceBatch verifies and stages every requested plugin before publishing any of
// them. Publication is rolled back as a unit if a later destination cannot be replaced.
func InstallMarketplaceBatch(ctx context.Context, requests []MarketplaceInstallRequest, destinationRoot string, reinstall bool, clientHTTP *http.Client, stderr io.Writer) ([]InstallationMarker, error) {
	releases := make([]Release, 0, len(requests))
	for _, request := range requests {
		release, err := FetchRelease(ctx, request.Source, request.ManifestDigest, clientHTTP)
		if err != nil {
			return nil, fmt.Errorf("fetching marketplace plugin %q: %w", request.Namespace, err)
		}
		if release.Manifest.Namespace != request.Namespace {
			return nil, fmt.Errorf("marketplace namespace %q conflicts with plugin manifest namespace %q", request.Namespace, release.Manifest.Namespace)
		}
		if release.Manifest.PluginVersion != request.PluginVersion {
			return nil, fmt.Errorf("marketplace version %q conflicts with plugin manifest version %q", request.PluginVersion, release.Manifest.PluginVersion)
		}
		releases = append(releases, release)
	}
	return installReleases(ctx, releases, destinationRoot, reinstall, stderr)
}

// preparedInstallation is one plugin's progress through the batch: staged on disk, then moved
// into place with whatever it displaced held in backup until the whole batch commits.
type preparedInstallation struct {
	marker    InstallationMarker
	stage     string
	target    string
	backup    string
	backedUp  bool
	installed bool
}

// installationOps are the destructive filesystem steps of publication, injected so a test can
// fail one of them part-way through a batch and exercise the rollback.
type installationOps struct {
	rename    func(string, string) error
	removeAll func(string) error
}

func installReleases(ctx context.Context, releases []Release, destinationRoot string, reinstall bool, stderr io.Writer) ([]InstallationMarker, error) {
	return installReleasesWithOps(ctx, releases, destinationRoot, reinstall, stderr, installationOps{rename: os.Rename, removeAll: os.RemoveAll})
}

// installReleasesWithOps runs the batch in four phases: reject what cannot commit, stage every
// release beside the destination, publish them as a unit, and only then drop the backups. No
// installed plugin is touched until staging has succeeded for all of them.
func installReleasesWithOps(ctx context.Context, releases []Release, destinationRoot string, reinstall bool, stderr io.Writer, ops installationOps) ([]InstallationMarker, error) {
	if len(releases) == 0 {
		return []InstallationMarker{}, nil
	}
	if err := checkInstallTargets(releases, destinationRoot, reinstall); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating plugin directory: %w", err)
	}
	prepared, err := stageReleases(ctx, releases, destinationRoot, stderr)
	if err != nil {
		return nil, err
	}
	// Publication clears the stage of everything it moves, so this drops only scratch
	// directories that never became an installation.
	defer removeStages(prepared)
	if err := publishInstallations(prepared, destinationRoot, reinstall, ops); err != nil {
		return nil, err
	}
	return discardBackups(prepared, ops)
}

// checkInstallTargets rejects a batch that cannot commit -- a namespace selected twice, or an
// existing installation the caller did not ask to replace -- before anything is downloaded.
func checkInstallTargets(releases []Release, destinationRoot string, reinstall bool) error {
	seen := make(map[string]struct{}, len(releases))
	for _, release := range releases {
		namespace := release.Manifest.Namespace
		if _, exists := seen[namespace]; exists {
			return fmt.Errorf("plugin %q was selected more than once", namespace)
		}
		seen[namespace] = struct{}{}
		if _, err := os.Stat(filepath.Join(destinationRoot, namespace)); err == nil && !reinstall {
			return fmt.Errorf("plugin %q is already installed; use --reinstall", namespace)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking plugin %q installation: %w", namespace, err)
		}
	}
	return nil
}

// stageReleases lays every release out in its own scratch directory. On failure it removes the
// stages it had already built, so the caller is handed either a complete batch or nothing.
func stageReleases(ctx context.Context, releases []Release, destinationRoot string, stderr io.Writer) ([]preparedInstallation, error) {
	prepared := make([]preparedInstallation, 0, len(releases))
	for index := range releases {
		item, err := prepareInstallation(ctx, releases[index], destinationRoot, stderr)
		// Staging copies the archive to disk, so the in-memory copy is dead weight for the
		// rest of the batch. Dropping it keeps peak usage at one archive instead of all of
		// them, which matters when a marketplace selection installs many plugins at once.
		releases[index].ArtifactData = nil
		releases[index].ManifestData = nil
		if err != nil {
			removeStages(prepared)
			return nil, fmt.Errorf("preparing plugin %q: %w", releases[index].Manifest.Namespace, err)
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func removeStages(prepared []preparedInstallation) {
	for _, item := range prepared {
		if item.stage != "" {
			_ = os.RemoveAll(item.stage)
		}
	}
}

// publishInstallations moves every staged plugin into its destination, setting aside whatever it
// replaces. A destination that cannot be replaced rolls the whole batch back.
func publishInstallations(prepared []preparedInstallation, destinationRoot string, reinstall bool, ops installationOps) error {
	for index := range prepared {
		item := &prepared[index]
		if reinstall {
			if err := backupInstallation(item, destinationRoot, ops); err != nil {
				return rollbackInstallations(prepared, ops, err)
			}
		}
		if err := ops.rename(item.stage, item.target); err != nil {
			return rollbackInstallations(prepared, ops, fmt.Errorf("publishing plugin %q: %w", item.marker.Namespace, err))
		}
		item.stage = ""
		item.installed = true
	}
	return nil
}

// backupInstallation moves an existing installation aside so publishing over it stays undoable.
// It does nothing when the destination is empty, which is the first-install case.
func backupInstallation(item *preparedInstallation, destinationRoot string, ops installationOps) error {
	if _, err := os.Stat(item.target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking plugin %q before commit: %w", item.marker.Namespace, err)
	}
	// MkdirTemp reserves an unused name; rename then needs the name itself to be free.
	backup, err := os.MkdirTemp(destinationRoot, ".plugin-backup-")
	if err != nil {
		return fmt.Errorf("creating backup for plugin %q: %w", item.marker.Namespace, err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("preparing backup for plugin %q: %w", item.marker.Namespace, err)
	}
	item.backup = backup
	if err := ops.rename(item.target, item.backup); err != nil {
		return fmt.Errorf("backing up plugin %q: %w", item.marker.Namespace, err)
	}
	item.backedUp = true
	return nil
}

// rollbackInstallations undoes a partial publication in reverse order, removing each new
// directory and moving its backup back. What it could not undo is reported alongside the cause.
func rollbackInstallations(prepared []preparedInstallation, ops installationOps, cause error) error {
	var rollbackErr error
	for index := len(prepared) - 1; index >= 0; index-- {
		item := &prepared[index]
		if item.installed {
			if err := ops.removeAll(item.target); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("removing new plugin %q: %w", item.marker.Namespace, err))
				continue
			}
			item.installed = false
		}
		if item.backedUp {
			if err := ops.rename(item.backup, item.target); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restoring plugin %q: %w", item.marker.Namespace, err))
				continue
			}
			item.backedUp = false
		}
	}
	return errors.Join(cause, rollbackErr)
}

// discardBackups drops the displaced installations once publication has committed. The markers
// come back even on failure: the new plugins are live, and only scratch directories are left over.
func discardBackups(prepared []preparedInstallation, ops installationOps) ([]InstallationMarker, error) {
	markers := make([]InstallationMarker, 0, len(prepared))
	var cleanupErr error
	for index := range prepared {
		item := &prepared[index]
		markers = append(markers, item.marker)
		if item.backedUp {
			if err := ops.removeAll(item.backup); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("removing backup for plugin %q: %w", item.marker.Namespace, err))
			}
			item.backedUp = false
		}
	}
	return markers, cleanupErr
}

func prepareInstallation(ctx context.Context, release Release, destinationRoot string, stderr io.Writer) (preparedInstallation, error) {
	stage, err := os.MkdirTemp(destinationRoot, ".plugin-stage-")
	if err != nil {
		return preparedInstallation{}, fmt.Errorf("creating plugin staging directory: %w", err)
	}
	fail := func(err error) (preparedInstallation, error) {
		return preparedInstallation{}, errors.Join(err, os.RemoveAll(stage))
	}
	executable, err := Extract(release, stage)
	if err != nil {
		return fail(err)
	}
	if err := verifyExecutable(ctx, executable, release.Manifest.Namespace, release.Manifest.Protocol, stderr); err != nil {
		return fail(err)
	}
	executableDigest, err := fileDigest(executable)
	if err != nil {
		return fail(fmt.Errorf("hashing plugin executable: %w", err))
	}
	marker := InstallationMarker{
		MarkerVersion:    currentMarkerVersion,
		ManifestVersion:  release.Manifest.Version,
		Namespace:        release.Manifest.Namespace,
		PluginVersion:    release.Manifest.PluginVersion,
		Protocol:         release.Manifest.Protocol,
		Source:           release.CanonicalSource,
		ManifestDigest:   release.ManifestDigest,
		ArtifactDigest:   release.Artifact.SHA256,
		ExecutableDigest: executableDigest,
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fail(fmt.Errorf("encoding plugin marker: %w", err))
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(stage, MarkerName), data, 0o600); err != nil {
		return fail(fmt.Errorf("writing plugin marker: %w", err))
	}
	return preparedInstallation{marker: marker, stage: stage, target: filepath.Join(destinationRoot, release.Manifest.Namespace)}, nil
}

// wellFormedMarker reports whether a decoded marker describes an installation this binary can
// reason about at all. Mismatches that deserve their own diagnostic -- the host platform, and the
// executable digest -- are left to the caller.
func wellFormedMarker(marker InstallationMarker, namespace string) bool {
	switch {
	case marker.MarkerVersion != 1 && marker.MarkerVersion != currentMarkerVersion:
		return false
	case marker.ManifestVersion != 1:
		return false
	case marker.Namespace != namespace:
		return false
	case marker.PluginVersion == "":
		return false
	case !supportedProtocol(marker.Protocol):
		return false
	case marker.Source == "":
		return false
	case !validDigest(marker.ManifestDigest) || !validDigest(marker.ArtifactDigest):
		return false
	}
	return true
}

// fileDigest hashes a file without holding it in memory. A plugin executable is bounded only by
// the archive limit, so reading one whole to hash it would spike usage by up to that limit on
// every install and on every launch-time validation.
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return InstallationMarker{}, fmt.Errorf("invalid plugin marker: trailing data")
	}
	if marker.MarkerVersion == 0 {
		marker.MarkerVersion = 1
	}
	if marker.Protocol == "" {
		marker.Protocol = ProtocolV1
	}
	if !wellFormedMarker(marker, namespace) {
		return InstallationMarker{}, fmt.Errorf("invalid plugin marker")
	}
	if marker.OS != runtime.GOOS || marker.Arch != runtime.GOARCH {
		return InstallationMarker{}, fmt.Errorf("plugin %q was installed for %s/%s, running on %s/%s", namespace, marker.OS, marker.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if marker.MarkerVersion == currentMarkerVersion && !validDigest(marker.ExecutableDigest) {
		return InstallationMarker{}, fmt.Errorf("invalid plugin marker")
	}
	expected := filepath.Join(directory, "wuko-plugin-"+namespace)
	info, err := os.Stat(expected)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return InstallationMarker{}, fmt.Errorf("invalid plugin installation")
	}
	if marker.MarkerVersion == currentMarkerVersion {
		executableDigest, err := fileDigest(expected)
		if err != nil {
			return InstallationMarker{}, fmt.Errorf("reading plugin executable: %w", err)
		}
		if executableDigest != marker.ExecutableDigest {
			return InstallationMarker{}, fmt.Errorf("plugin executable digest mismatch")
		}
	}
	return marker, nil
}
