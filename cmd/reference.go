package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/reference"
	"github.com/up2jj/wuko/step"
)

func newReferenceCmd() *cobra.Command {
	var format string
	var catalog *reference.Catalog
	load := func() (*reference.Catalog, error) {
		if catalog != nil {
			return catalog, nil
		}
		// A private registry prevents installed plugins or injected runtime steps
		// from changing the offline built-in reference.
		registry := step.NewRegistry()
		if err := registerBuiltinSteps(registry); err != nil {
			return nil, err
		}
		var err error
		catalog, err = reference.New(registry.Definitions())
		return catalog, err
	}
	command := &cobra.Command{
		Use: "reference", Short: "Explore built-in steps, examples, and workflow topics offline",
		Long:    "Read the reference shipped with this Wuko binary. Lookups never run steps or plugins, load configuration, or create example files.",
		Example: "  wuko reference step\n  wuko reference step http --format json\n  wuko reference topic agents",
		Args:    cobra.NoArgs, Annotations: map[string]string{referenceAnnotation: "true"},
	}
	command.PersistentFlags().StringVar(&format, "format", "text", "reference output format: text or json")
	_ = command.RegisterFlagCompletionFunc("format", formatCompletion("text", "json"))
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := referenceFormat(format, false); err != nil {
			return err
		}
		entries := []reference.Entry{
			{ID: "step", Kind: "index", Summary: "Built-in configuration, outputs, and examples", Command: "wuko reference step"},
			{ID: "topic", Kind: "index", Summary: "Workflow syntax, execution, templates, and agent guidance", Command: "wuko reference topic"},
			{ID: "example", Kind: "index", Summary: "Complete workflows and supporting files, never executed automatically", Command: "wuko reference example TYPE"},
			{ID: "search", Kind: "index", Summary: "Find steps and topics using all query words", Command: "wuko reference search QUERY"},
		}
		return writeReferenceIndex(cmd.OutOrStdout(), format, "index", entries)
	}
	stepCmd := &cobra.Command{Use: "step [TYPE]", Short: "List built-in steps or describe one step", Args: cobra.MaximumNArgs(1), Example: "  wuko reference step\n  wuko reference step shell --format json"}
	stepCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := referenceFormat(format, false); err != nil {
			return err
		}
		c, err := load()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return writeReferenceIndex(cmd.OutOrStdout(), format, "steps", c.Steps())
		}
		item, err := c.Step(args[0])
		if err != nil {
			return err
		}
		if format == "json" {
			return reference.WriteJSON(cmd.OutOrStdout(), "step", item)
		}
		var out strings.Builder
		fmt.Fprintf(&out, "%s\n\n%s\n\nConfiguration fields:\n", item.ID, item.Summary)
		writeReferenceFields(&out, item.Fields, "  ")
		out.WriteString("\nRules:\n")
		for _, rule := range item.Rules {
			fmt.Fprintf(&out, "  %s\n", rule)
		}
		fmt.Fprintf(&out, "\nOutputs:\n  %s", item.Outputs.Kind)
		if item.Outputs.Open {
			out.WriteString(" (dynamic fields; see documentation)")
		}
		out.WriteString("\n\nExamples:\n")
		for _, example := range item.Examples {
			fmt.Fprintf(&out, "  %s: %s\n    %s\n", example.ID, example.Summary, example.Command)
		}
		fmt.Fprintf(&out, "\nDocumentation (%s):\n\n%s\n\nRelated help:\n", item.Source, item.Documentation)
		for _, id := range item.Related {
			fmt.Fprintf(&out, "  wuko reference topic %s\n", id)
		}
		_, err = io.WriteString(cmd.OutOrStdout(), out.String())
		return err
	}
	topicCmd := &cobra.Command{Use: "topic [ID]", Short: "List authoring topics or read one topic", Args: cobra.MaximumNArgs(1), Example: "  wuko reference topic\n  wuko reference topic templates --format json"}
	topicCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := referenceFormat(format, false); err != nil {
			return err
		}
		c, err := load()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return writeReferenceIndex(cmd.OutOrStdout(), format, "topics", c.Topics())
		}
		item, err := c.Topic(args[0])
		if err != nil {
			return err
		}
		if format == "json" {
			return reference.WriteJSON(cmd.OutOrStdout(), "topic", item)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), item.Documentation)
		return err
	}
	searchCmd := &cobra.Command{Use: "search QUERY...", Short: "Search built-in step and topic documentation", Args: cobra.MinimumNArgs(1), Example: "  wuko reference search 'HTTP request'\n  wuko reference search 'write file' --format json"}
	searchCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := referenceFormat(format, false); err != nil {
			return err
		}
		query := strings.Join(args, " ")
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("search query must not be empty")
		}
		c, err := load()
		if err != nil {
			return err
		}
		return writeReferenceIndex(cmd.OutOrStdout(), format, "search", c.Search(query))
	}
	exampleCmd := &cobra.Command{Use: "example TYPE [ID]", Short: "List a step's examples or read a complete example", Args: cobra.RangeArgs(1, 2), Example: "  wuko reference example file\n  wuko reference example set basic --format yaml\n  wuko reference example shell basic --format json"}
	exampleCmd.Flags().StringVar(&format, "format", "text", "example output format: text, json, or yaml (single-file examples only)")
	_ = exampleCmd.RegisterFlagCompletionFunc("format", formatCompletion("text", "json", "yaml"))
	exampleCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := referenceFormat(format, true); err != nil {
			return err
		}
		c, err := load()
		if err != nil {
			return err
		}
		item, err := c.Step(args[0])
		if err != nil {
			return err
		}
		if len(args) == 1 {
			if format == "yaml" {
				return fmt.Errorf("--format yaml requires an example ID; use 'wuko reference example %s'", args[0])
			}
			return writeReferenceIndex(cmd.OutOrStdout(), format, "examples", item.Examples)
		}
		examples, err := c.Examples(args[0])
		if err != nil {
			return err
		}
		for _, example := range examples {
			if example.ID != args[1] {
				continue
			}
			if format == "json" {
				return reference.WriteJSON(cmd.OutOrStdout(), "example", example)
			}
			if format == "yaml" {
				if len(example.Files) != 0 {
					return fmt.Errorf("example %s/%s requires supporting files; use --format json for the complete bundle", item.ID, example.ID)
				}
				_, err = io.WriteString(cmd.OutOrStdout(), example.Workflow)
				return err
			}
			var out strings.Builder
			fmt.Fprintf(&out, "%s\n\n%s\n", example.Title, example.Description)
			if len(example.Prerequisites) > 0 {
				out.WriteString("\nPrerequisites:\n")
				for _, line := range example.Prerequisites {
					fmt.Fprintf(&out, "  %s\n", line)
				}
			}
			fmt.Fprintf(&out, "\nworkflow.yaml:\n\n%s\nExpected result:\n  %s\n", example.Workflow, example.Expected)
			if len(example.Files) > 0 {
				out.WriteString("\nSupporting files are included in --format json. Save each file relative to workflow.yaml.\n")
			}
			out.WriteString("\nUsage (from the directory containing workflow.yaml):\n  wuko --no-config validate --file ./workflow.yaml\n  wuko --no-config run --file ./workflow.yaml --once\n")
			_, err = io.WriteString(cmd.OutOrStdout(), out.String())
			return err
		}
		return fmt.Errorf("unknown example %q for %s; use 'wuko reference example %s'", args[1], args[0], args[0])
	}
	for _, child := range []*cobra.Command{stepCmd, topicCmd, searchCmd, exampleCmd} {
		command.AddCommand(child)
	}
	stepCmd.ValidArgsFunction = referenceCompletion(load, "step")
	topicCmd.ValidArgsFunction = referenceCompletion(load, "topic")
	exampleCmd.ValidArgsFunction = referenceCompletion(load, "example")
	return command
}

func referenceFormat(format string, yaml bool) error {
	if format == "text" || format == "json" || (yaml && format == "yaml") {
		return nil
	}
	if yaml {
		return fmt.Errorf("format must be text, json, or yaml")
	}
	return fmt.Errorf("format must be text or json")
}

func formatCompletion(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func referenceCompletion(load func() (*reference.Catalog, error), kind string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		c, err := load()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		entries := c.Steps()
		if kind == "topic" {
			entries = c.Topics()
		}
		if kind == "example" && len(args) == 1 {
			item, err := c.Step(args[0])
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			entries = nil
			entries = append(entries, item.Examples...)
		} else if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		values := []string{}
		for _, entry := range entries {
			if strings.HasPrefix(entry.ID, prefix) {
				values = append(values, entry.ID+"\t"+entry.Summary)
			}
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func writeReferenceIndex(writer io.Writer, format, kind string, entries []reference.Entry) error {
	if format == "json" {
		return reference.WriteJSON(writer, kind, entries)
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(writer, "No matches.")
		return err
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for _, entry := range entries {
		fmt.Fprintf(table, "%s\t%s\n  %s\n", entry.ID, entry.Summary, entry.Command)
	}
	return table.Flush()
}

func writeReferenceFields(out *strings.Builder, fields []reference.Field, indent string) {
	for _, field := range fields {
		details := field.Type
		if field.Required != nil && *field.Required {
			details += "; required"
		}
		if field.Default != nil {
			details += fmt.Sprintf("; default %v", field.Default)
		}
		if len(field.Choices) > 0 {
			details += "; " + strings.Join(field.Choices, " | ")
		}
		fmt.Fprintf(out, "%s%s: %s\n", indent, field.Name, details)
		writeReferenceFields(out, field.Fields, indent+"  ")
		if field.Items != nil {
			writeReferenceFields(out, []reference.Field{*field.Items}, indent+"  ")
		}
	}
}
