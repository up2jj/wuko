package workflow

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// ActionSource identifies a Wuko action loaded from HTTPS, a GitHub repository,
// a plugin, a local path, or a command.
type ActionSource struct {
	URL     string
	Path    string
	GitHub  string
	Plugin  string
	Token   string
	Command string
	Args    []string
}

func parsePluginActionLocator(locator string) (string, string, error) {
	parts := strings.Split(locator, "/")
	if len(parts) != 2 || !ValidPluginNamespace(parts[0]) || !identifierPattern.MatchString(parts[1]) {
		return "", "", fmt.Errorf("plugin action must use plugin:namespace/action with valid identifiers")
	}
	return parts[0], parts[1], nil
}

func (source *ActionSource) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("uses must be a non-empty HTTPS URL, relative path, plugin action, GitHub-hosted Wuko action locator, or command object")
		}
		if locator, ok := strings.CutPrefix(node.Value, "plugin:"); ok {
			if !strings.Contains(locator, "{{") {
				if _, _, err := parsePluginActionLocator(locator); err != nil {
					return err
				}
			}
			source.Plugin = locator
			return nil
		}
		if strings.HasPrefix(node.Value, "https://") || strings.HasPrefix(node.Value, "http://") {
			source.URL = node.Value
			return nil
		}
		// A scalar may also name a GitHub-hosted action, either with the github: prefix Display
		// round-trips or in the bare owner/repo[@ref]:directory shape. Object-form uses stays the
		// only way to pass a token.
		if locator, prefixed := strings.CutPrefix(node.Value, "github:"); prefixed || looksLikeGitHubWukoActionLocator(node.Value) {
			if !strings.Contains(locator, "{{") {
				if _, err := parseGitHubWukoActionLocator(locator); err != nil {
					return err
				}
			}
			source.GitHub = locator
			return nil
		}
		source.Path = node.Value
		return nil
	case yaml.MappingNode:
		allowed := map[string]bool{"command": true, "args": true, "github": true, "token": true}
		present := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			name := node.Content[i].Value
			if !allowed[name] {
				return fmt.Errorf("field %s not found in action source", name)
			}
			present[name] = true
		}
		var raw struct {
			Command string   `yaml:"command"`
			Args    []string `yaml:"args,omitempty"`
			GitHub  string   `yaml:"github"`
			Token   string   `yaml:"token,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if present["github"] {
			if present["command"] || present["args"] {
				return fmt.Errorf("uses github cannot be combined with command or args")
			}
			if strings.TrimSpace(raw.GitHub) == "" {
				return fmt.Errorf("uses github is required")
			}
			if present["token"] && strings.TrimSpace(raw.Token) == "" {
				return fmt.Errorf("uses token must not be empty")
			}
			if !strings.Contains(raw.GitHub, "{{") {
				if _, err := parseGitHubWukoActionLocator(raw.GitHub); err != nil {
					return err
				}
			}
			source.GitHub, source.Token = raw.GitHub, raw.Token
			return nil
		}
		if present["token"] {
			return fmt.Errorf("uses token requires github")
		}
		if strings.TrimSpace(raw.Command) == "" {
			return fmt.Errorf("uses command is required")
		}
		source.Command, source.Args = raw.Command, raw.Args
		return nil
	default:
		return fmt.Errorf("uses must be a non-empty HTTPS URL, relative path, plugin action, GitHub-hosted Wuko action locator, or command object")
	}
}

// Empty reports whether no action source was declared.
func (source ActionSource) Empty() bool {
	return source.URL == "" && source.Path == "" && source.GitHub == "" && source.Plugin == "" && source.Command == ""
}

// Display returns a safe description that excludes command arguments and URL query strings.
func (source ActionSource) Display() string {
	if source.URL != "" {
		parsed, err := url.Parse(source.URL)
		if err == nil {
			parsed.RawQuery, parsed.Fragment = "", ""
			return parsed.String()
		}
		return source.URL
	}
	if source.Path != "" {
		return source.Path
	}
	if source.GitHub != "" {
		return "github:" + source.GitHub
	}
	if source.Plugin != "" {
		return "plugin:" + source.Plugin
	}
	return source.Command
}
