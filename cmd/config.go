package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/up2jj/wuko/internal/yamlstrict"
	"go.yaml.in/yaml/v3"
)

type localConfigDocument struct {
	Flags    map[string]any             `yaml:"flags"`
	Default  *defaultInvocation         `yaml:"default"`
	Commands map[string]commandDefaults `yaml:"commands"`
}

type commandDefaults struct {
	Flags    map[string]any             `yaml:"flags"`
	Commands map[string]commandDefaults `yaml:"commands"`
}

type defaultInvocation struct {
	Workflow configString   `yaml:"workflow"`
	Target   configString   `yaml:"target"`
	Flags    map[string]any `yaml:"flags"`
}

// YAML normally coerces numbers and booleans into struct string fields. Selectors must
// be actual strings, just like string-valued flags in the configuration.
type configString string

func (value *configString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("line %d: expected a string", node.Line)
	}
	*value = configString(node.Value)
	return nil
}

// cliOnlyAnnotation marks flags that confirm or scope a removal. A config file found in
// the working directory is repository content, so it must not be able to remove anything
// without the user asking for it on the command line.
const cliOnlyAnnotation = "wuko_cli_only"

func markCLIOnly(command *cobra.Command, names ...string) {
	for _, name := range names {
		if err := command.Flags().SetAnnotation(name, cliOnlyAnnotation, []string{"true"}); err != nil {
			panic(err)
		}
	}
}

type localConfiguration struct {
	document   localConfigDocument
	configured map[*pflag.Flag]bool
}

type localConfigurationKey struct{}

// Keep pflag.Changed reserved for explicit CLI input. Some commands additionally need
// to distinguish an absent option from an explicitly configured empty or false value.
func optionProvided(command *cobra.Command, name string) bool {
	flag := command.Flags().Lookup(name)
	return flag != nil && (flag.Changed || optionConfigured(command, flag))
}

func optionConfigured(command *cobra.Command, flag *pflag.Flag) bool {
	if command.Context() == nil {
		return false
	}
	configuration, _ := command.Context().Value(localConfigurationKey{}).(*localConfiguration)
	return configuration != nil && configuration.configured[flag]
}

func wrapLocalConfiguration(command *cobra.Command, configuration *localConfiguration, deps dependencies) {
	if command.Hidden || command.Name() == "completion" {
		return
	}
	if command.Runnable() {
		validateArgs := command.Args
		command.Args = func(command *cobra.Command, args []string) error {
			if err := configuration.apply(command, deps); err != nil {
				// A bare invocation runs a workflow only through default:. Without one no
				// run was selected, so there is no run to report.
				if command == command.Root() && configuration.document.Default == nil {
					return err
				}
				return reportEarlyRunFailure(command, err)
			}
			if validateArgs != nil {
				return validateArgs(command, args)
			}
			return nil
		}
	}
	for _, child := range command.Commands() {
		wrapLocalConfiguration(child, configuration, deps)
	}
}

func (configuration *localConfiguration) apply(command *cobra.Command, deps dependencies) error {
	configuration.document = localConfigDocument{}
	configuration.configured = make(map[*pflag.Flag]bool)
	command.SetContext(context.WithValue(command.Context(), localConfigurationKey{}, configuration))
	path, err := localConfigPath(command, deps)
	if err != nil {
		return err
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading CLI config %s: %w", path, err)
		}
		if err := yamlstrict.Decode(data, &configuration.document); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("decoding CLI config %s: %w", path, err)
		}
		if err := configuration.document.validate(command.Root()); err != nil {
			return fmt.Errorf("CLI config %s: %w", path, err)
		}
	}
	values := make(map[string]any)
	maps.Copy(values, configuration.document.Flags)
	if command == command.Root() {
		if preset := configuration.document.Default; preset != nil {
			maps.Copy(values, configuration.document.Commands["run"].Flags)
			maps.Copy(values, preset.Flags)
		} else {
			var supplied string
			command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
				if flag.Changed {
					supplied = flag.Name
				}
			})
			if supplied != "" {
				return fmt.Errorf("--%s requires default.workflow in CLI config; use wuko run WORKFLOW instead", supplied)
			}
		}
	} else {
		var names []string
		for current := command; current != command.Root(); current = current.Parent() {
			names = append(names, current.Name())
		}
		commands := configuration.document.Commands
		for index, name := range slices.Backward(names) {
			node := commands[name]
			// Only options on the selected command are applied. Parent command local
			// flags do not become child defaults.
			if index == 0 {
				maps.Copy(values, node.Flags)
			}
			commands = node.Commands
		}
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		flag := command.Flags().Lookup(name)
		if flag == nil {
			return fmt.Errorf("CLI config %s: flags.%s: not an option of %s", path, name, command.CommandPath())
		}
		if flag.Changed {
			continue
		}
		if name == "env-loader" && deps.getenv("WUKO_ENV_LOADERS") != "" {
			continue
		}
		value := values[name]
		if flag.Value.Type() == "stringArray" {
			err = flag.Value.(pflag.SliceValue).Replace(configStrings(value))
		} else {
			err = flag.Value.Set(fmt.Sprint(value))
		}
		if err != nil {
			return fmt.Errorf("CLI config %s: flags.%s: invalid option value", path, name)
		}
		configuration.configured[flag] = true
	}
	return nil
}

func localConfigPath(command *cobra.Command, deps dependencies) (string, error) {
	flags := command.Root().PersistentFlags()
	explicit := flags.Changed("config")
	path, _ := flags.GetString("config")
	disabled, _ := flags.GetBool("no-config")
	if explicit && disabled {
		return "", fmt.Errorf("--config cannot be used with --no-config")
	}
	if disabled {
		return "", nil
	}
	if explicit && strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("--config must name a YAML file")
	}
	cwd := deps.cwd
	if cwd == nil {
		cwd = os.Getwd
	}
	directory, err := cwd()
	if err != nil {
		return "", fmt.Errorf("finding CLI config directory: %w", err)
	}
	if explicit {
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		return path, nil
	}
	var found string
	for _, name := range []string{"wuko.yml", "wuko.yaml"} {
		candidate := filepath.Join(directory, name)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("finding CLI config %s: %w", candidate, err)
		}
		if found != "" {
			return "", fmt.Errorf("both wuko.yml and wuko.yaml exist in %s; select one with --config or use --no-config", directory)
		}
		found = candidate
	}
	return found, nil
}

func (document localConfigDocument) validate(root *cobra.Command) error {
	if err := validateConfigFlags(document.Flags, root.PersistentFlags(), "flags"); err != nil {
		return err
	}
	if preset := document.Default; preset != nil {
		if strings.TrimSpace(string(preset.Workflow)) == "" {
			return fmt.Errorf("default.workflow must be a nonempty string")
		}
		if err := validateConfigFlags(preset.Flags, root.LocalNonPersistentFlags(), "default.flags"); err != nil {
			return err
		}
	}
	return validateCommandDefaults(document.Commands, root, "commands")
}

func validateCommandDefaults(commands map[string]commandDefaults, parent *cobra.Command, path string) error {
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		node := commands[name]
		var command *cobra.Command
		for _, child := range parent.Commands() {
			if child.Name() == name && !child.Hidden {
				command = child
				break
			}
		}
		location := path + "." + name
		if command == nil {
			return fmt.Errorf("%s: unknown public command", location)
		}
		if err := validateConfigFlags(node.Flags, command.LocalNonPersistentFlags(), location+".flags"); err != nil {
			return err
		}
		if err := validateCommandDefaults(node.Commands, command, location+".commands"); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigFlags(values map[string]any, flags *pflag.FlagSet, path string) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		value := values[name]
		flag := flags.Lookup(name)
		if flag == nil || flag.Hidden || name == "config" || name == "no-config" || name == "help" || name == "version" {
			return fmt.Errorf("%s.%s: unknown or non-configurable flag", path, name)
		}
		if _, ok := flag.Annotations[cliOnlyAnnotation]; ok {
			return fmt.Errorf("%s.%s: must be passed on the command line", path, name)
		}
		valid := false
		switch flag.Value.Type() {
		case "bool":
			_, valid = value.(bool)
		case "string":
			_, valid = value.(string)
		case "stringArray":
			list, ok := value.([]any)
			valid = ok
			for _, item := range list {
				if _, ok := item.(string); !ok {
					valid = false
				}
			}
		}
		if !valid {
			return fmt.Errorf("%s.%s: expected %s", path, name, flag.Value.Type())
		}
	}
	return nil
}

func configStrings(value any) []string {
	if list, ok := value.([]string); ok {
		return list
	}
	var result []string
	for _, item := range value.([]any) {
		result = append(result, item.(string))
	}
	return result
}
