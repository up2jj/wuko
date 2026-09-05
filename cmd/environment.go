package cmd

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	envload "github.com/up2jj/wuko/environment"
	"github.com/up2jj/wuko/provider"
)

func invocationEnvironment(command *cobra.Command, deps dependencies, cwd string) (envload.InvocationEnvironment, error) {
	policy, err := invocationEnvironmentPolicy(command, deps)
	if err != nil {
		return envload.InvocationEnvironment{}, err
	}
	base := processEnvironment()
	result := envload.InvocationEnvironment{Values: base, Loaders: []string{}}
	if deps.environment != nil {
		result, err = deps.environment.Load(command.Context(), cwd, base, policy)
		if err != nil {
			return envload.InvocationEnvironment{}, err
		}
	}
	diagnostic.Emit(diagnosticsFor(command, deps, cwd), diagnostic.Event{
		Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusDetail, Message: "loaded invocation environment",
		Attributes: []diagnostic.Attribute{
			diagnostic.Attr("environment_policy", policy.String()),
			diagnostic.Attr("environment_loaders", strings.Join(result.Loaders, ",")),
		},
	})
	return result, nil
}

func invocationEnvironmentPolicy(command *cobra.Command, deps dependencies) (envload.Policy, error) {
	flags := command.Root().PersistentFlags()
	if flags.Changed("env-loader") {
		names, err := flags.GetStringArray("env-loader")
		if err != nil {
			return envload.Policy{}, fmt.Errorf("reading --env-loader: %w", err)
		}
		return envload.ParsePolicy(splitLoaderNames(names))
	}
	// deps.getenv is defaulted by newRootCmd, but helpers such as installWorkflow are
	// also driven directly with a bare dependencies value.
	getenv := deps.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return envload.ParsePolicyValue(getenv("WUKO_ENV_LOADERS"))
}

// splitLoaderNames flattens comma-separated values so --env-loader mise,direnv reads
// the same as the repeated flag and as WUKO_ENV_LOADERS.
func splitLoaderNames(values []string) []string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, strings.Split(value, ",")...)
	}
	return names
}

func processEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			environment[name] = value
		}
	}
	return environment
}

func environmentValues(result envload.InvocationEnvironment) (map[string]string, []string) {
	return result.Values, slices.Clone(result.Loaders)
}

func invocationProviders(command *cobra.Command, deps dependencies, environment map[string]string) (provider.Set, error) {
	if deps.providers == nil {
		return provider.Set{}, nil
	}
	return deps.providers.Load(command.Context(), environment)
}
