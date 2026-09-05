package workflow

import (
	"context"
	"fmt"
	"net/url"
	"path"
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
	SHA256 string         `yaml:"sha256" json:"sha256"`
	With   map[string]any `yaml:"with,omitempty" json:"with,omitempty"`
}

func (source *PluginSource) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("plugin declaration must be an object")
	}
	if err := rejectUnknownFields(node, "plugin declaration", map[string]bool{"source": true, "sha256": true, "with": true}); err != nil {
		return err
	}
	type plain PluginSource
	if err := node.Decode((*plain)(source)); err != nil {
		return err
	}
	source.Source = strings.TrimSpace(source.Source)
	source.SHA256 = strings.ToLower(strings.TrimSpace(source.SHA256))
	if source.Source == "" {
		return fmt.Errorf("plugin source must not be empty")
	}
	if !validSHA256(source.SHA256) {
		return fmt.Errorf("plugin sha256 must be 64 lowercase hexadecimal characters")
	}
	if _, err := source.CanonicalSource(); err != nil {
		return err
	}
	return nil
}

// CanonicalSource normalizes the two supported workflow-scoped remote locator forms.
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
	parsed, err := url.Parse(source.Source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
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

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
