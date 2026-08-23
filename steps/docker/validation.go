package docker

import (
	"fmt"
	"path"
	"reflect"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/up2jj/wuko/workflow"
)

const (
	operationRun           = "run"
	operationBuild         = "build"
	operationPull          = "pull"
	operationPush          = "push"
	operationTag           = "tag"
	operationInspect       = "inspect"
	operationHealthWait    = "health_wait"
	operationLogin         = "login"
	operationNetworkCreate = "network_create"
	operationVolumeCreate  = "volume_create"
	operationVerifyDigest  = "verify_digest"
)

func operation(config Config) string {
	if config.Operation == "" {
		return operationRun
	}
	return config.Operation
}

func cleanupEnabled(config Config) bool { return config.Cleanup == nil || *config.Cleanup }

func managesResource(config Config) bool {
	op := operation(config)
	return cleanupEnabled(config) && (op == operationNetworkCreate || op == operationVolumeCreate)
}

func validateConfig(config Config, configuredFields ...map[string]bool) error {
	present := inferredFields(config)
	if len(configuredFields) > 0 && configuredFields[0] != nil {
		present = configuredFields[0]
	}
	op := operation(config)
	if templated(op) {
		return nil
	}

	allowed := map[string]bool{"operation": true}
	allow := func(fields ...string) {
		for _, field := range fields {
			allowed[field] = true
		}
	}
	switch op {
	case operationRun:
		allow("image", "command", "args", "working_directory", "mounts", "env", "network", "user", "platform", "pull", "tty", "stdin")
		if err := validateRunConfig(config, present); err != nil {
			return err
		}
	case operationPull, operationPush:
		allow("image", "platform", "auth")
		if strings.TrimSpace(config.Image) == "" {
			return fmt.Errorf("image is required for %s", op)
		}
		if err := validatePlatformValue(config.Platform); err != nil {
			return err
		}
		if config.Auth != nil {
			if err := validateAuth(*config.Auth, false); err != nil {
				return err
			}
		}
	case operationTag:
		allow("source", "target")
		if strings.TrimSpace(config.Source) == "" {
			return fmt.Errorf("source is required for tag")
		}
		if strings.TrimSpace(config.Target) == "" {
			return fmt.Errorf("target is required for tag")
		}
	case operationInspect:
		allow("image", "platform")
		if strings.TrimSpace(config.Image) == "" {
			return fmt.Errorf("image is required for inspect")
		}
		if err := validatePlatformValue(config.Platform); err != nil {
			return err
		}
	case operationHealthWait:
		allow("container")
		if strings.TrimSpace(config.Container) == "" {
			return fmt.Errorf("container is required for health_wait")
		}
	case operationLogin:
		allow("auth")
		if config.Auth == nil {
			return fmt.Errorf("auth is required for login")
		}
		if err := validateAuth(*config.Auth, true); err != nil {
			return err
		}
	case operationNetworkCreate:
		allow("name", "driver", "internal", "attachable", "options", "labels", "cleanup")
		if strings.TrimSpace(config.Name) == "" {
			return fmt.Errorf("name is required for network_create")
		}
		if err := validateLabels(config.Labels); err != nil {
			return err
		}
	case operationVolumeCreate:
		allow("name", "driver", "driver_options", "labels", "cleanup")
		if strings.TrimSpace(config.Name) == "" {
			return fmt.Errorf("name is required for volume_create")
		}
		if err := validateLabels(config.Labels); err != nil {
			return err
		}
	case operationVerifyDigest:
		allow("image", "expected_digest", "platform")
		if strings.TrimSpace(config.Image) == "" {
			return fmt.Errorf("image is required for verify_digest")
		}
		if !templated(config.ExpectedDigest) {
			if _, err := digest.Parse(config.ExpectedDigest); err != nil {
				return fmt.Errorf("expected_digest must be a valid OCI digest: %w", err)
			}
		}
		if err := validatePlatformValue(config.Platform); err != nil {
			return err
		}
	case operationBuild:
		allow("context", "dockerfile", "tags", "platforms", "output", "build_args", "target", "pull", "no_cache", "cache_from", "cache_to")
		if len(config.Tags) == 0 {
			return fmt.Errorf("tags is required for build")
		}
		for i, tag := range config.Tags {
			if strings.TrimSpace(tag) == "" {
				return fmt.Errorf("tag %d must not be empty", i+1)
			}
		}
		if !templated(config.Output) && config.Output != "load" && config.Output != "push" {
			return fmt.Errorf("output must be load or push for build")
		}
		if config.Output == "load" && len(config.Platforms) > 1 {
			return fmt.Errorf("output load supports at most one platform")
		}
		for _, value := range config.Platforms {
			if templated(value) {
				continue
			}
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("build platforms must not contain empty values")
			}
			if _, err := parsePlatform(value); err != nil {
				return fmt.Errorf("invalid build platform %q: %w", value, err)
			}
		}
		if present["pull"] {
			if _, ok := config.Pull.(bool); !ok {
				return fmt.Errorf("pull must be a boolean for build")
			}
		}
		for _, descriptor := range append(append([]string(nil), config.CacheFrom...), config.CacheTo...) {
			if strings.TrimSpace(descriptor) == "" {
				return fmt.Errorf("build cache descriptors must not be empty")
			}
		}
	default:
		return fmt.Errorf("operation must be run, build, pull, push, tag, inspect, health_wait, login, network_create, volume_create, or verify_digest")
	}

	for field := range present {
		if !allowed[field] {
			return fmt.Errorf("%s is not allowed for %s", field, op)
		}
	}
	return nil
}

func validateRunConfig(config Config, present map[string]bool) error {
	if strings.TrimSpace(config.Image) == "" {
		return fmt.Errorf("image is required")
	}
	if config.Command == "" && len(config.Args) > 0 {
		return fmt.Errorf("args require command")
	}
	if config.WorkingDirectory != "" && !templated(config.WorkingDirectory) && !path.IsAbs(config.WorkingDirectory) {
		return fmt.Errorf("working_directory must be an absolute container path")
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return fmt.Errorf("invalid environment name %q", key)
		}
	}
	for i, configured := range config.Mounts {
		if strings.TrimSpace(configured.Source) == "" {
			return fmt.Errorf("mount %d source is required", i+1)
		}
		if configured.Type != "" && !templated(configured.Type) && configured.Type != "bind" && configured.Type != "volume" {
			return fmt.Errorf("mount %d type must be bind or volume", i+1)
		}
		if !templated(configured.Target) && !path.IsAbs(configured.Target) {
			return fmt.Errorf("mount %d target must be an absolute container path", i+1)
		}
	}
	policy := ""
	if present["pull"] {
		var ok bool
		policy, ok = config.Pull.(string)
		if !ok {
			return fmt.Errorf("pull must be a policy string for run")
		}
	}
	if templated(policy) {
		return validatePlatformValue(config.Platform)
	}
	switch normalizePullPolicy(policy) {
	case "never", "if-missing", "always":
	default:
		return fmt.Errorf("pull must be one of never, if-missing, or always")
	}
	return validatePlatformValue(config.Platform)
}

func validatePlatformValue(value string) error {
	if templated(value) {
		return nil
	}
	_, err := parsePlatform(value)
	return err
}

func validateAuth(auth Auth, requireServer bool) error {
	if templated(auth.Username) || templated(auth.Password) || templated(auth.ServerAddress) ||
		templated(auth.IdentityToken) || templated(auth.RegistryToken) {
		return nil
	}
	if requireServer && strings.TrimSpace(auth.ServerAddress) == "" {
		return fmt.Errorf("auth server_address is required for login")
	}
	passwordMode := auth.Username != "" || auth.Password != ""
	if passwordMode && (auth.Username == "" || auth.Password == "") {
		return fmt.Errorf("auth username and password must be supplied together")
	}
	modes := 0
	if passwordMode {
		modes++
	}
	if auth.IdentityToken != "" {
		modes++
	}
	if auth.RegistryToken != "" {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("auth requires exactly one of username/password, identity_token, or registry_token")
	}
	return nil
}

func validateLabels(labels map[string]string) error {
	for _, key := range []string{managedLabel, ownerHostLabel, ownerPIDLabel, workflowLabel, stepLabel, ownershipLabel} {
		if _, exists := labels[key]; exists {
			return fmt.Errorf("label %q is reserved by Wuko", key)
		}
	}
	return nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func inferredFields(config Config) map[string]bool {
	value := reflect.ValueOf(config)
	typeOfConfig := value.Type()
	result := make(map[string]bool)
	for i := range value.NumField() {
		field := typeOfConfig.Field(i)
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if name != "" && !value.Field(i).IsZero() {
			result[name] = true
		}
	}
	return result
}
