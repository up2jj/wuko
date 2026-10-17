package workflow

import "context"

// ResolvedPluginSource carries a verified runtime pin, separately from YAML declarations.
type ResolvedPluginSource struct {
	Source   string
	Identity string
	SHA256   string
	With     map[string]any
}

func (s ResolvedPluginSource) CanonicalSource() (string, error) {
	return (PluginSource{Source: s.Source}).CanonicalSource()
}

// PluginOrigin identifies the declaring workflow before temporary locations are remapped.
type PluginOrigin struct {
	Dir    string
	Root   string
	Source string
	Remote bool
}

// PluginSourceResolver resolves manifest pins before plugin helpers are initialized.
type PluginSourceResolver interface {
	ResolvePluginSources(context.Context, PluginOrigin, map[string]PluginSource) (map[string]ResolvedPluginSource, error)
}

// PluginLockPolicy is inherited by dependency and child-workflow loading.
type PluginLockPolicy struct{ Frozen, ReadOnly, Remote bool }
type pluginPolicyKey struct{}
type resolvedPluginsKey struct{}

func ContextWithPluginLockPolicy(ctx context.Context, policy PluginLockPolicy) context.Context {
	old := PluginLockPolicyFromContext(ctx)
	policy.Remote = policy.Remote || old.Remote
	policy.Frozen = policy.Frozen || old.Frozen
	policy.ReadOnly = policy.ReadOnly || old.ReadOnly
	return context.WithValue(ctx, pluginPolicyKey{}, policy)
}
func PluginLockPolicyFromContext(ctx context.Context) PluginLockPolicy {
	policy, _ := ctx.Value(pluginPolicyKey{}).(PluginLockPolicy)
	return policy
}
func ContextWithResolvedPlugins(ctx context.Context, sources map[string]ResolvedPluginSource) context.Context {
	return context.WithValue(ctx, resolvedPluginsKey{}, sources)
}
func ResolvedPluginsFromContext(ctx context.Context) map[string]ResolvedPluginSource {
	sources, _ := ctx.Value(resolvedPluginsKey{}).(map[string]ResolvedPluginSource)
	return sources
}
func (d *Definition) ResolvedPlugins() map[string]ResolvedPluginSource {
	if d.resolvedPlugins != nil || len(d.Plugins) == 0 {
		return d.resolvedPlugins
	}
	return d.unpinnedPlugins()
}

// unpinnedPlugins mirrors declarations when no resolver supplied pins.
func (d *Definition) unpinnedPlugins() map[string]ResolvedPluginSource {
	result := make(map[string]ResolvedPluginSource, len(d.Plugins))
	for namespace, source := range d.Plugins {
		result[namespace] = ResolvedPluginSource{Source: source.Source, With: source.With}
	}
	return result
}
func (d *Definition) PluginContext(ctx context.Context) context.Context {
	ctx = ContextWithPluginLockPolicy(ctx, PluginLockPolicy{Remote: d.pluginRemote})
	return ContextWithResolvedPlugins(ContextWithPlugins(ctx, d.Plugins), d.ResolvedPlugins())
}

// InheritPluginResolution keeps composite actions on the enclosing workflow's pins.
func (d *Definition) InheritPluginResolution(parent *Definition) {
	d.resolvedPlugins = parent.ResolvedPlugins()
}

// SourceIdentity is stable across temporary copies of a remote workflow package.
func (s ResolvedPluginSource) SourceIdentity() string {
	if s.Identity != "" {
		return s.Identity
	}
	return s.Source
}

// PluginOriginSource returns the logical source used while materializing remote packages.
func (d *Definition) PluginOriginSource() string { return d.sourceLabel }
