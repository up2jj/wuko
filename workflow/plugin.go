package workflow

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type pluginContextKey struct{}

// ContextWithPlugins binds the enclosing workflow's plugin declarations to registry resolution.
func ContextWithPlugins(ctx context.Context, plugins map[string]PluginSource) context.Context {
	return context.WithValue(ctx, pluginContextKey{}, plugins)
}

// PluginsFromContext returns declarations bound by the engine.
func PluginsFromContext(ctx context.Context) map[string]PluginSource {
	plugins, _ := ctx.Value(pluginContextKey{}).(map[string]PluginSource)
	return plugins
}

var pluginNamespacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// PluginSource declares an authoritative workflow-scoped plugin release manifest.
type PluginSource struct {
	Source string         `yaml:"source" json:"source"`
	With   map[string]any `yaml:"with,omitempty" json:"with,omitempty"`
}

func (source *PluginSource) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("plugin declaration must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "sha256" {
			return fmt.Errorf("inline plugin sha256 is no longer supported; run wuko plugin lock FILE --migrate")
		}
	}
	if err := rejectUnknownFields(node, "plugin declaration", map[string]bool{"source": true, "with": true}); err != nil {
		return err
	}
	type plain PluginSource
	if err := node.Decode((*plain)(source)); err != nil {
		return err
	}
	source.Source = strings.TrimSpace(source.Source)
	if source.Source == "" {
		return fmt.Errorf("plugin source must not be empty")
	}
	if _, err := source.CanonicalSource(); err != nil {
		return err
	}
	return nil
}

// CanonicalSource normalizes remote locators and explicit local bundle paths.
func (source PluginSource) CanonicalSource() (string, error) {
	if strings.HasPrefix(source.Source, "github:") {
		value := strings.TrimPrefix(source.Source, "github:")
		at := strings.IndexByte(value, '@')
		if at <= 0 || at == len(value)-1 || len(strings.Split(value[:at], "/")) != 2 {
			return "", fmt.Errorf("GitHub plugin source requires owner/repository@ref")
		}
		tail := value[at+1:]
		if !strings.Contains(tail, ":") {
			value += ":plugin.json"
		} else {
			parts := strings.SplitN(tail, ":", 2)
			if parts[0] == "" || parts[1] == "" || path.IsAbs(parts[1]) || path.Clean(parts[1]) != parts[1] || strings.HasPrefix(parts[1], "../") {
				return "", fmt.Errorf("invalid GitHub plugin manifest path")
			}
		}
		return "github:" + value, nil
	}
	if !strings.HasPrefix(source.Source, "https://") {
		// A Windows volume such as C: is part of an absolute local path, not a URL scheme.
		volume := filepath.VolumeName(source.Source)
		if source.Source == "" || strings.Contains(source.Source[len(volume):], ":") {
			return "", fmt.Errorf("plugin source must be a local bundle, HTTPS, or github:owner/repository@ref[:path]")
		}
		local := filepath.Clean(source.Source)
		if filepath.Ext(local) != ".json" {
			local = filepath.Join(local, "plugin.json")
		}
		return local, nil
	}
	parsed, err := url.Parse(source.Source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("workflow plugin source must be HTTPS or github:owner/repository@ref[:path]")
	}
	if parsed.Path == "" || strings.HasSuffix(parsed.Path, "/") || !strings.Contains(path.Base(parsed.Path), ".") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/plugin.json"
	}
	return parsed.String(), nil
}

// ValidPluginNamespace reports whether namespace is suitable for a namespaced plugin type.
func ValidPluginNamespace(namespace string) bool {
	return pluginNamespacePattern.MatchString(namespace)
}
