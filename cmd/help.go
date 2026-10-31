package cmd

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/up2jj/wuko/reference"
)

const referenceAnnotation = "wuko_reference"

type commandHelp struct {
	Path        string           `json:"path"`
	Summary     string           `json:"summary"`
	Description string           `json:"description"`
	Usage       string           `json:"usage"`
	Aliases     []string         `json:"aliases"`
	Arguments   []argumentHelp   `json:"arguments"`
	Commands    []commandSummary `json:"commands"`
	Examples    []string         `json:"examples"`
	Flags       []flagHelp       `json:"flags"`
	GlobalFlags []flagHelp       `json:"global_flags"`
	Behavior    string           `json:"behavior"`
	Related     []string         `json:"related"`
}

type commandSummary struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Group   string `json:"group,omitempty"`
}

type argumentHelp struct {
	Syntax      string `json:"syntax"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Repeatable  bool   `json:"repeatable"`
}

type flagHelp struct {
	Name        string              `json:"name"`
	Shorthand   string              `json:"shorthand,omitempty"`
	Type        string              `json:"type"`
	Default     string              `json:"default"`
	Description string              `json:"description"`
	Repeatable  bool                `json:"repeatable"`
	Choices     []string            `json:"choices,omitempty"`
	Constraints map[string][]string `json:"constraints,omitempty"`
}

func configureHelp(root *cobra.Command) {
	groups := []struct {
		id, title string
		names     []string
	}{
		{"workflows", "Workflows", []string{"run", "ui", "list", "tree", "validate"}},
		{"packages", "Packages and Plugins", []string{"install", "uninstall", "plugin", "marketplace"}},
		{"integrations", "Integrations", []string{"git", "agent", "shim", "completion"}},
		{"help", "Help and Reference", []string{"help", "reference"}},
	}
	for _, group := range groups {
		root.AddGroup(&cobra.Group{ID: group.id, Title: group.title})
		for _, child := range root.Commands() {
			if slices.Contains(group.names, child.Name()) {
				child.GroupID = group.id
			}
		}
	}
	help := &cobra.Command{
		Use: "help [COMMAND...]", Short: "Show command help as text or JSON", GroupID: "help",
		Long:        "Describe a command without running it or loading local configuration. JSON describes built-in defaults, not effective invocation settings.",
		Example:     "  wuko help run\n  wuko help plugin install --format json",
		Annotations: map[string]string{referenceAnnotation: "true"},
	}
	var format string
	help.Flags().StringVar(&format, "format", "text", "help output format: text or json")
	help.RunE = func(command *cobra.Command, args []string) error {
		if err := referenceFormat(format, false); err != nil {
			return err
		}
		target, remaining, err := root.Find(args)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			return fmt.Errorf("unknown command path %q; use 'wuko help'", strings.Join(args, " "))
		}
		for current := target; current != nil; current = current.Parent() {
			if current.Hidden {
				return fmt.Errorf("unknown public command %q", strings.Join(args, " "))
			}
		}
		if format == "json" {
			return reference.WriteJSON(command.OutOrStdout(), "command", describeCommand(target))
		}
		return writeCommandHelp(command.OutOrStdout(), target)
	}
	help.ValidArgsFunction = func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		target, remaining, err := root.Find(args)
		if err != nil || len(remaining) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var values []string
		for _, child := range target.Commands() {
			if !child.Hidden && strings.HasPrefix(child.Name(), prefix) {
				values = append(values, child.Name()+"\t"+child.Short)
			}
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
	_ = help.RegisterFlagCompletionFunc("format", formatCompletion("text", "json"))
	root.SetHelpCommand(help)
	root.SetHelpCommandGroupID("help")
	root.SetHelpFunc(func(command *cobra.Command, _ []string) {
		if err := writeCommandHelp(command.OutOrStdout(), command); err != nil {
			fmt.Fprintln(command.ErrOrStderr(), err)
		}
	})
	root.SetUsageFunc(func(command *cobra.Command) error { return writeCommandHelp(command.ErrOrStderr(), command) })
}

func describeCommand(command *cobra.Command) commandHelp {
	command.InitDefaultVersionFlag()
	command.InitDefaultHelpFlag()
	description := command.Long
	if description == "" {
		description = command.Short
	}
	doc := commandHelp{Path: command.CommandPath(), Summary: command.Short, Description: description, Usage: command.UseLine(), Aliases: append([]string{}, command.Aliases...), Arguments: []argumentHelp{}, Commands: []commandSummary{}, Examples: []string{}, Flags: describeFlags(command.LocalNonPersistentFlags()), GlobalFlags: describeFlags(command.InheritedFlags()), Behavior: commandBehavior(command), Related: []string{"wuko help", "wuko reference", "wuko reference topic agents"}}
	// Persistent flags belong to the command declaring them. Root run flags stay
	// hidden because their availability depends on a configured default workflow.
	for _, flag := range describeFlags(command.PersistentFlags()) {
		if !slices.ContainsFunc(doc.Flags, func(existing flagHelp) bool { return existing.Name == flag.Name }) {
			doc.Flags = append(doc.Flags, flag)
		}
	}
	slices.SortFunc(doc.Flags, func(a, b flagHelp) int { return strings.Compare(a.Name, b.Name) })
	for _, token := range strings.Fields(command.Use)[1:] {
		doc.Arguments = append(doc.Arguments, argumentHelp{Syntax: token, Description: argumentDescription(token), Required: !strings.HasPrefix(token, "["), Repeatable: strings.Contains(token, "...")})
	}
	for _, child := range command.Commands() {
		if child.Hidden {
			continue
		}
		doc.Commands = append(doc.Commands, commandSummary{Path: child.CommandPath(), Summary: child.Short, Group: child.GroupID})
	}
	for _, example := range strings.Split(strings.TrimSpace(command.Example), "\n") {
		if example = strings.TrimSpace(example); example != "" {
			doc.Examples = append(doc.Examples, example)
		}
	}
	if len(doc.Examples) == 0 {
		doc.Examples = commandExamples(command)
	}
	return doc
}

func describeFlags(flags *pflag.FlagSet) []flagHelp {
	result := []flagHelp{}
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		f := flagHelp{Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(), Default: flag.DefValue, Description: flag.Usage, Repeatable: strings.Contains(flag.Value.Type(), "Array") || strings.Contains(flag.Value.Type(), "Slice")}
		for key, values := range flag.Annotations {
			name := map[string]string{cobra.BashCompOneRequiredFlag: "required", "cobra_annotation_required_if_others_set": "required_together", "cobra_annotation_one_required": "one_required", "cobra_annotation_mutually_exclusive": "mutually_exclusive", cliOnlyAnnotation: "cli_only"}[key]
			if name != "" {
				if f.Constraints == nil {
					f.Constraints = map[string][]string{}
				}
				f.Constraints[name] = append([]string{}, values...)
			}
		}
		switch flag.Name {
		case "env-loader":
			f.Choices = []string{"auto", "none", "mise", "asdf", "direnv"}
		case "format":
			if strings.Contains(flag.Usage, "yaml") {
				f.Choices = []string{"text", "json", "yaml"}
			} else {
				f.Choices = []string{"text", "json"}
			}
		case "reporter":
			if flag.Value.Type() == "string" {
				f.Choices = []string{"plain", "github"}
			} else {
				f.Choices = []string{"plain", "github", "multiplexer"}
			}
		}
		result = append(result, f)
	})
	return result
}

func argumentDescription(token string) string {
	name := strings.TrimSuffix(strings.Trim(token, "[]"), "...")
	switch {
	case strings.Contains(name, "NAME|URL|GITHUB"):
		return "Discovered workflow name, HTTPS URL, or github:owner/repo@ref:path locator; with --file, the positional argument selects a target"
	case name == "TARGET":
		return "Target declared by the selected workflow"
	case name == "NAME":
		return "Workflow or package name; use the matching list or status command to discover values"
	case name == "AGENT":
		return "Available coding-agent name from wuko agent list; omit to install for every available agent"
	case strings.HasPrefix(name, "COMMAND"):
		return "Public command path, for example plugin install; omit for root help"
	case name == "TYPE":
		return "Built-in step type from wuko reference step"
	case name == "ID":
		return "Stable topic or example ID from the corresponding reference index"
	case name == "QUERY":
		return "Search terms; all words must match, ignoring case"
	case strings.Contains(name, "SOURCE") || strings.Contains(name, "URL|GITHUB|FILE"):
		return "Local source path or supported remote source locator"
	case strings.Contains(name, "FILE"):
		return "Workflow file path; repeat when multiple files are accepted"
	case name == "NAMESPACE":
		return "Plugin namespace"
	case strings.Contains(name, "DIRECTORY"):
		return "Destination project directory"
	case strings.Contains(name, "HOOK"):
		return "Git hook name, such as pre-commit or pre-push"
	case strings.Contains(name, "GIT_ARGS"):
		return "Arguments supplied by Git to the hook"
	case strings.Contains(name, "bash|"):
		return "Shell to generate completion for"
	default:
		return "Command argument: " + strings.ToLower(strings.TrimSuffix(name, "..."))
	}
}

func commandBehavior(command *cobra.Command) string {
	path := strings.TrimPrefix(command.CommandPath(), "wuko")
	if path == "" {
		return "With a configured default workflow, runs that workflow and accepts run flags (see wuko run --help). Otherwise opens the workflow picker in a terminal or lists workflows without one. Help does not load configuration."
	}
	if strings.HasPrefix(path, " help") || strings.HasPrefix(path, " reference") {
		return "Offline and non-interactive. Ignores local configuration. Text is the default; JSON is one schema_version: 1 document on stdout. Errors use stderr and a nonzero exit status. Does not run workflows, plugins, or examples."
	}
	switch path {
	case " list":
		return "Lists effective workflows; --all includes shadowed sources. --format json emits workflow-list schema_version: 1 on stdout; discovery warnings go to stderr. Does not execute steps."
	case " run":
		return "Executes trusted workflow code. --file - reads stdin. --dry-run previews without running steps, but loading can execute source resolvers or plugin helpers. --once ignores cron repetition. --yes answers confirmation prompts only; --defaults uses declared prompt defaults. --report-json - reserves stdout for one execution-report schema_version: 2 document and sends workflow output to stderr. Exit status 0 means success, 1 failure, and 130 cancellation."
	case " validate":
		return "Validates one workflow, an explicit --file, or all effective workflows. --file - reads stdin. --format json emits diagnostics on stdout, including validation failures. Does not run steps, but loading can execute source resolvers or plugin helpers."
	case " tree":
		return "Displays expanded workflow structure without running steps. --file - reads stdin. Loading can execute source resolvers or plugin helpers."
	case " ui":
		return "Starts a local browser form and executes the submitted workflow. Requires an explicit workflow form."
	}
	if path == " shim uninstall" {
		return "Removes the named Wuko shims from ~/.wuko/bin, or --dir, without prompting. Refuses a file Wuko did not generate and leaves workflows and projects in place. After the last shim in that directory is removed, cleans up unchanged PATH entries managed by Wuko; preserves user-modified entries."
	}
	if strings.Contains(path, " uninstall") || strings.HasSuffix(path, " remove") {
		return "Removes the selected installation or source. May prompt for confirmation; use --yes only when the removal is intended. Scope and confirmation flags must be supplied explicitly where marked CLI-only."
	}
	if strings.HasPrefix(path, " agent list") {
		return "Lists supported coding-agent executables found on PATH and their skill installation directories. Does not install skills."
	}
	if strings.HasPrefix(path, " completion") {
		return "Writes a shell completion script to stdout. Does not load local configuration."
	}
	if behavior, ok := map[string]string{
		" shim install":               "Writes an executable shell script to ~/.wuko/bin, or --dir, that calls wuko on PATH. A discovered NAME or --file is pinned to its absolute path; --by-name resolves NAME where the shim runs. Flags after -- are baked into workflow shims. --project DIR --as NAME binds the full CLI to a project and configures PATH for Bash, Zsh, or Fish unless --no-path is supplied. Refuses replacement without --force and never replaces a file Wuko did not generate. Does not run workflows.",
		" shim list":                  "Lists Wuko shims in ~/.wuko/bin, or --dir, with their workflow or project binding. Marks missing workflow files and project workflow directories. Ignores other files. Does not run workflows.",
		" agent install":              "Installs the bundled skills into the selected available agent's skill directory. Omitting AGENT installs for all supported agents found on PATH. Writes an installation summary to stdout.",
		" install":                    "Installs a workflow into the local directory or the home-global directory with --global. Marketplace sources may prompt for package selection; repeated --package flags select packages explicitly. Installation can run declared workflow lifecycle steps.",
		" plugin install":             "Installs executable plugin releases into the local or global plugin directory. Marketplace sources may prompt for selection; --package selects namespaces explicitly. --reinstall replaces an existing installation.",
		" plugin lock":                "Verifies declared workflow plugins and writes pins to wuko.lock. Requires the intended workflow files and access to their declared plugin sources.",
		" marketplace init":           "Creates a marketplace manifest in the invocation directory and prints its path. Existing files are not silently replaced.",
		" marketplace build":          "Builds published workflow and plugin artifacts and rebuilds the marketplace manifest. --check checks consistency without publishing changes.",
		" marketplace workflow add":   "Adds a workflow package source to the current marketplace. Requires an initialized marketplace and a valid local package source.",
		" marketplace plugin init":    "Creates a plugin project from the selected embedded template. --list-templates prints available templates without scaffolding a project.",
		" marketplace plugin add":     "Imports a complete plugin release into the current marketplace and updates its source metadata.",
		" marketplace plugin update":  "Replaces an imported plugin's release from SOURCE in the current marketplace.",
		" marketplace plugin release": "Builds a local plugin with its wuko.release.yaml workflow, validates the bundle, and publishes marketplace artifacts. --skip-build publishes an already built bundle without running that workflow.",
		" git hook init":              "Creates a starter .wuko/git-hooks.yaml manifest and workflows in the current repository. Prints created paths; review them before installing hooks.",
		" git hook install":           "Installs Wuko dispatchers for hooks declared in .wuko/git-hooks.yaml. Existing unmanaged hooks require the documented explicit chaining policy.",
		" git hook uninstall":         "Removes Wuko-managed hook dispatchers for selected hooks, or all managed hooks when none are supplied.",
		" git hook status":            "Prints Wuko Git hook installation status without running workflow steps.",
		" git hook run":               "Runs workflows configured for HOOK using the Git arguments and hook context. Executes trusted workflow code and returns failure when a configured workflow fails.",
	}[path]; ok {
		return behavior
	}
	if !command.Runnable() {
		return "Command group. Select a subcommand to perform an operation; use wuko help COMMAND --format json to inspect its interface."
	}
	return "Uses local CLI configuration unless --no-config is supplied; explicit flags override configured defaults. Command errors go to stderr and return a nonzero exit status. See the examples and wuko reference topic cli for the operation's prerequisites and effects."
}

func commandExamples(command *cobra.Command) []string {
	path := strings.TrimPrefix(command.CommandPath(), "wuko ")
	examples := map[string][]string{
		"wuko":       {"wuko list --format json", "wuko help run", "wuko reference topic agents"},
		"list":       {"wuko list", "wuko list --all --format json"},
		"agent":      {"wuko agent list", "wuko agent install codex"},
		"agent list": {"wuko agent list"}, "agent install": {"wuko agent install codex", "wuko agent install"},
		"install":              {"wuko install ./workflow.yaml", "wuko install --global https://example.com/workflow.yaml"},
		"uninstall":            {"wuko uninstall hello", "wuko uninstall hello --yes"},
		"plugin":               {"wuko plugin install ./plugin-release", "wuko plugin lock ./workflow.yaml"},
		"plugin lock":          {"wuko plugin lock ./workflow.yaml"},
		"plugin uninstall":     {"wuko plugin uninstall hello", "wuko plugin uninstall hello --yes"},
		"marketplace":          {"wuko marketplace init", "wuko marketplace build --check"},
		"marketplace init":     {"wuko marketplace init"},
		"marketplace workflow": {"wuko marketplace workflow add ../workflows/release"},
		"marketplace plugin":   {"wuko marketplace plugin init hello --template go"},
		"git":                  {"wuko git hook status", "wuko git hook init"},
		"git hook":             {"wuko git hook status", "wuko git hook install"},
		"git hook init":        {"wuko git hook init"}, "git hook install": {"wuko git hook install"},
		"git hook uninstall": {"wuko git hook uninstall pre-commit"},
		"git hook status":    {"wuko git hook status"}, "git hook run": {"wuko git hook run pre-commit"},
		"completion":   {"wuko completion bash", "wuko completion zsh"},
		"shim":         {"wuko shim install check", "wuko shim install --project ./automation --as mytools", "wuko shim list"},
		"shim install": {"wuko shim install check", "wuko shim install deploy staging --as deploy-staging", "wuko shim install lint -- --no-config", "wuko shim install --project ./automation --as mytools"},
		"shim list":    {"wuko shim list"}, "shim uninstall": {"wuko shim uninstall check"},
	}
	if values, ok := examples[path]; ok {
		return values
	}
	return []string{command.CommandPath() + " --help"}
}

func writeCommandHelp(writer io.Writer, command *cobra.Command) error {
	doc := describeCommand(command)
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\nUsage:\n  %s\n", doc.Description, doc.Usage)
	if command.Runnable() && len(doc.Commands) > 0 {
		fmt.Fprintf(&out, "  %s [command]\n", doc.Path)
	}
	if len(doc.Arguments) > 0 {
		out.WriteString("\nArguments:\n")
		for _, arg := range doc.Arguments {
			fmt.Fprintf(&out, "  %s\n      %s\n", arg.Syntax, arg.Description)
		}
	}
	if len(doc.Commands) > 0 {
		groups := command.Groups()
		if len(groups) == 0 {
			groups = []*cobra.Group{{ID: "", Title: "Commands"}}
		}
		for _, group := range groups {
			fmt.Fprintf(&out, "\n%s:\n", strings.TrimSuffix(group.Title, ":"))
			table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
			for _, child := range doc.Commands {
				if child.Group == group.ID || len(command.Groups()) == 0 {
					fmt.Fprintf(table, "  %s\t%s\n", strings.TrimPrefix(child.Path, command.CommandPath()+" "), child.Summary)
				}
			}
			if err := table.Flush(); err != nil {
				return err
			}
		}
	}
	if len(doc.Examples) > 0 {
		out.WriteString("\nExamples:\n")
		for _, example := range doc.Examples {
			fmt.Fprintf(&out, "  %s\n", example)
		}
	}
	for _, group := range []struct {
		title string
		flags []flagHelp
	}{{"Flags", doc.Flags}, {"Global Flags", doc.GlobalFlags}} {
		if len(group.flags) == 0 {
			continue
		}
		fmt.Fprintf(&out, "\n%s:\n", group.title)
		table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
		for _, flag := range group.flags {
			name := "    --" + flag.Name
			if flag.Shorthand != "" {
				name = "-" + flag.Shorthand + ", --" + flag.Name
			}
			if flag.Type != "bool" {
				name += " " + flag.Type
			}
			usage := flag.Description
			if flag.Default != "" && flag.Default != "false" && flag.Default != "[]" {
				usage += fmt.Sprintf(" (default %q)", flag.Default)
			}
			fmt.Fprintf(table, "  %s\t%s\n", name, usage)
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintf(&out, "\nBehavior:\n  %s\n\nRelated help:\n", doc.Behavior)
	for _, related := range doc.Related {
		fmt.Fprintf(&out, "  %s\n", related)
	}
	_, err := io.WriteString(writer, out.String())
	return err
}
