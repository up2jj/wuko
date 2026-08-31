// Package secret resolves workflow secret references through external password-manager CLIs.
package secret

import (
	"fmt"
	"regexp"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config declares ordered provider authentication checks.
type Config struct {
	EnsureAuth []AuthConfig `yaml:"ensure_auth,omitempty"`
}

// AuthConfig configures authentication for one provider.
type AuthConfig struct {
	Provider string      `yaml:"provider"`
	Login    LoginConfig `yaml:"login,omitempty"`
}

// LoginConfig controls optional native and fallback authentication.
type LoginConfig struct {
	Native   bool            `yaml:"native,omitempty"`
	Fallback *FallbackConfig `yaml:"fallback,omitempty"`
}

// FallbackConfig runs either a command or a shell script. When SessionEnv is set, trimmed
// stdout is kept in the provider's private environment under that name.
type FallbackConfig struct {
	Command    string   `yaml:"command,omitempty"`
	Args       []string `yaml:"args,omitempty"`
	Script     string   `yaml:"script,omitempty"`
	Shell      string   `yaml:"shell,omitempty"`
	SessionEnv string   `yaml:"session_env,omitempty"`
}

// Validate checks the intrinsic secret configuration shape.
func (config Config) Validate() error {
	seen := make(map[string]struct{}, len(config.EnsureAuth))
	for i, auth := range config.EnsureAuth {
		provider := strings.TrimSpace(auth.Provider)
		if provider != auth.Provider {
			return fmt.Errorf("secrets.ensure_auth[%d].provider must not contain surrounding whitespace", i)
		}
		if provider != "op" && provider != "bw" {
			return fmt.Errorf("secrets.ensure_auth[%d].provider must be op or bw", i)
		}
		if _, exists := seen[provider]; exists {
			return fmt.Errorf("secrets.ensure_auth contains duplicate provider %q", provider)
		}
		seen[provider] = struct{}{}
		if auth.Login.Fallback == nil {
			continue
		}
		fallback := auth.Login.Fallback
		hasCommand := strings.TrimSpace(fallback.Command) != ""
		hasScript := strings.TrimSpace(fallback.Script) != ""
		if hasCommand == hasScript {
			return fmt.Errorf("secrets.ensure_auth[%d].login.fallback requires exactly one of command or script", i)
		}
		if fallback.Shell != "" && !hasScript {
			return fmt.Errorf("secrets.ensure_auth[%d].login.fallback.shell requires script", i)
		}
		if fallback.SessionEnv != "" && !environmentNamePattern.MatchString(fallback.SessionEnv) {
			return fmt.Errorf("secrets.ensure_auth[%d].login.fallback.session_env is not a valid environment name", i)
		}
	}
	return nil
}
