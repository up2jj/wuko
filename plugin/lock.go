package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/up2jj/wuko/internal/filelock"
	"github.com/up2jj/wuko/workflow"
	"go.yaml.in/yaml/v3"
)

const LockfileName = "wuko.lock"

type lockEntry struct {
	Namespace string `yaml:"namespace"`
	Source    string `yaml:"source"`
	SHA256    string `yaml:"sha256"`
}
type lockDocument struct {
	Version int         `yaml:"version"`
	Plugins []lockEntry `yaml:"plugins"`
}

func (e lockEntry) key() string { return e.Namespace + "\x00" + e.Source }

func readLock(path string) (lockDocument, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return lockDocument{Version: 1}, nil
	}
	if err != nil {
		return lockDocument{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var doc lockDocument
	if err := decoder.Decode(&doc); err != nil {
		return doc, fmt.Errorf("invalid %s: %w", LockfileName, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return doc, fmt.Errorf("invalid %s: expected one YAML document", LockfileName)
	}
	if doc.Version != 1 {
		return doc, fmt.Errorf("unsupported %s version %d", LockfileName, doc.Version)
	}
	seen := make(map[string]bool)
	for _, entry := range doc.Plugins {
		if !validNamespace(entry.Namespace) || !validDigest(entry.SHA256) || entry.Source == "" {
			return doc, fmt.Errorf("invalid %s plugin entry", LockfileName)
		}
		canonical, err := (workflow.PluginSource{Source: entry.Source}).CanonicalSource()
		if err != nil || lockSource(canonical) != entry.Source {
			return doc, fmt.Errorf("invalid %s plugin source", LockfileName)
		}
		if seen[entry.key()] {
			return doc, fmt.Errorf("duplicate %s plugin entry", LockfileName)
		}
		seen[entry.key()] = true
	}
	return doc, nil
}

func lockSource(source string) string {
	if strings.HasPrefix(source, "https://") {
		return diagnosticPluginSource(source)
	}
	if strings.HasPrefix(source, "github:") {
		return source
	}
	normalized := filepath.ToSlash(filepath.Clean(source))
	if !strings.HasPrefix(normalized, ".") && !filepath.IsAbs(source) {
		normalized = "./" + normalized
	}
	return normalized
}

func sourceForOrigin(origin workflow.PluginOrigin, source workflow.PluginSource) (string, string, error) {
	canonical, err := source.CanonicalSource()
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(canonical, "https://") || strings.HasPrefix(canonical, "github:") {
		return canonical, lockSource(canonical), nil
	}
	if !filepath.IsAbs(canonical) {
		canonical = filepath.Join(origin.Dir, canonical)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", "", err
	}
	if origin.Remote {
		root, err := filepath.EvalSymlinks(origin.Root)
		if err != nil {
			return "", "", err
		}
		resolved, err := filepath.EvalSymlinks(canonical)
		if err != nil {
			return "", "", err
		}
		if !within(root, resolved) {
			return "", "", fmt.Errorf("local plugin bundle escapes remote workflow package")
		}
	}
	relative, err := filepath.Rel(origin.Dir, canonical)
	if err != nil {
		return "", "", err
	}
	return canonical, lockSource(relative), nil
}

func (m *Manager) ResolvePluginSources(ctx context.Context, origin workflow.PluginOrigin, sources map[string]workflow.PluginSource) (map[string]workflow.ResolvedPluginSource, error) {
	return m.LockPluginSources(ctx, origin, sources, false, nil)
}

// LockPluginSources resolves pins without initializing plugins. Legacy pins are
// accepted only by the explicit migration command, never by workflow YAML loading.
func (m *Manager) LockPluginSources(ctx context.Context, origin workflow.PluginOrigin, sources map[string]workflow.PluginSource, update bool, legacy map[string]string) (map[string]workflow.ResolvedPluginSource, error) {
	var requests []pluginRequest
	for _, namespace := range slices.Sorted(maps.Keys(sources)) {
		requests = append(requests, pluginRequest{namespace: namespace, source: sources[namespace], legacy: legacy[namespace]})
	}
	resolved, err := m.resolveRequests(ctx, origin, requests, update)
	if err != nil {
		return nil, err
	}
	result := make(map[string]workflow.ResolvedPluginSource, len(requests))
	for i, request := range requests {
		result[request.namespace] = resolved[i]
	}
	return result, nil
}

type pluginRequest struct {
	namespace string
	source    workflow.PluginSource
	legacy    string
}

func (m *Manager) resolveRequests(ctx context.Context, origin workflow.PluginOrigin, requests []pluginRequest, update bool) ([]workflow.ResolvedPluginSource, error) {
	policy := workflow.PluginLockPolicyFromContext(ctx)
	remote := origin.Remote || policy.Remote
	if remote && policy.Frozen {
		return nil, fmt.Errorf("--frozen-lockfile is only supported for local workflows; save and lock the workflow locally")
	}
	result := make([]workflow.ResolvedPluginSource, 0, len(requests))
	if len(requests) == 0 {
		return result, nil
	}
	path := filepath.Join(origin.Dir, LockfileName)
	var doc lockDocument
	var err error
	if !remote {
		doc, err = readLock(path)
		if err != nil {
			return nil, err
		}
		// Only a run that may add or refresh pins takes the lock and re-reads under it, so
		// fully locked workflows never create lock files or serialize on the directory.
		if !policy.Frozen && !policy.ReadOnly && lockMayChange(origin, doc, requests, update) {
			handle, err := filelock.Acquire(ctx, path+".lock")
			if err != nil {
				return nil, err
			}
			defer handle.Release()
			doc, err = readLock(path)
			if err != nil {
				return nil, err
			}
		}
	}
	entries := make(map[string]lockEntry)
	for _, entry := range doc.Plugins {
		entries[entry.key()] = entry
	}
	selected := make(map[string]string)
	changed := false
	for _, request := range requests {
		namespace, source := request.namespace, request.source
		if !validNamespace(namespace) {
			return nil, fmt.Errorf("invalid plugin namespace %q", namespace)
		}
		fetchSource, identity, err := sourceForOrigin(origin, source)
		if err != nil {
			return nil, err
		}
		entry := lockEntry{Namespace: namespace, Source: identity}
		previous, exists := entries[entry.key()]
		pin := previous.SHA256
		if request.legacy != "" {
			if !validDigest(request.legacy) {
				return nil, fmt.Errorf("invalid legacy plugin sha256 for %q", namespace)
			}
			if exists && pin != request.legacy {
				return nil, fmt.Errorf("legacy plugin %q conflicts with %s", namespace, LockfileName)
			}
			pin = request.legacy
		}
		if !remote && (!exists || update) && (policy.Frozen || policy.ReadOnly) {
			return nil, fmt.Errorf("missing plugin pin for %q in %s; run wuko plugin lock FILE", namespace, path)
		}
		var resolvedDigest string
		if selected[entry.key()] != "" {
			resolvedDigest = selected[entry.key()]
		} else {
			var release Release
			if !remote && exists && !update && pin != "" {
				// Existing pins are only checked for presence; bring verifies archive bytes.
				release.ManifestDigest = pin
				err = m.store.present(ctx, namespace, fetchSource, pin)
			} else if remote {
				release, err = m.store.fresh(ctx, namespace, fetchSource)
			} else if update || pin == "" {
				release, err = m.store.obtain(ctx, namespace, fetchSource, "")
			} else {
				release, err = m.store.pinned(ctx, namespace, fetchSource, pin)
			}
			if err != nil {
				return nil, fmt.Errorf("resolving plugin %q: %w", namespace, err)
			}
			if !exists || update {
				if err := ValidateArtifact(release.Artifact, release.ArtifactData); err != nil {
					return nil, fmt.Errorf("validating plugin %q: %w", namespace, err)
				}
			}
			resolvedDigest = release.ManifestDigest
			selected[entry.key()] = resolvedDigest
		}
		resolved := workflow.ResolvedPluginSource{Source: fetchSource, SHA256: resolvedDigest, With: source.With}
		if origin.Remote && !strings.HasPrefix(fetchSource, "https://") && !strings.HasPrefix(fetchSource, "github:") {
			relative, err := filepath.Rel(origin.Root, fetchSource)
			if err != nil {
				return nil, err
			}
			resolved.Identity = diagnosticPluginSource(origin.Source) + "::" + filepath.ToSlash(relative)
		}
		result = append(result, resolved)
		if !remote && (!exists || update) {
			entry.SHA256 = resolvedDigest
			entries[entry.key()] = entry
			changed = true
		}
	}
	if changed {
		doc = lockDocument{Version: 1, Plugins: make([]lockEntry, 0, len(entries))}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			doc.Plugins = append(doc.Plugins, entries[key])
		}
		data, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := writeAtomic(path, data); err != nil {
			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return result, nil
}

func lockMayChange(origin workflow.PluginOrigin, doc lockDocument, requests []pluginRequest, update bool) bool {
	if update {
		return true
	}
	entries := make(map[string]bool, len(doc.Plugins))
	for _, entry := range doc.Plugins {
		entries[entry.key()] = true
	}
	for _, request := range requests {
		_, identity, err := sourceForOrigin(origin, request.source)
		if err != nil || !entries[lockEntry{Namespace: request.namespace, Source: identity}.key()] {
			return true
		}
	}
	return false
}
