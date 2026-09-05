package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
)

var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func newPluginCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Create and manage executable plugins"}
	command.AddCommand(newPluginInitCmd(), newPluginInstallCmd(deps), newPluginUninstallCmd(deps))
	return command
}

func newPluginInstallCmd(deps dependencies) *cobra.Command {
	var global, reinstall bool
	command := &cobra.Command{Use: "install SOURCE", Short: "Install a plugin release for the current platform", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		cwd, home, _, err := directories(deps)
		if err != nil {
			return err
		}
		root := filepath.Join(cwd, ".wuko", "plugins")
		if global {
			root = filepath.Join(home, ".wuko", "plugins")
		}
		marker, err := pluginpkg.Install(command.Context(), args[0], root, reinstall, nil, command.ErrOrStderr())
		if err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Installed plugin %s %s in %s\n", marker.Namespace, marker.PluginVersion, filepath.Join(root, marker.Namespace))
		return nil
	}}
	command.Flags().BoolVar(&global, "global", false, "install in the user plugin directory")
	command.Flags().BoolVar(&reinstall, "reinstall", false, "replace an existing installation")
	return command
}

func newPluginUninstallCmd(deps dependencies) *cobra.Command {
	var global, yes bool
	command := &cobra.Command{Use: "uninstall NAMESPACE", Short: "Uninstall a validated plugin installation", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		namespace := args[0]
		if !pluginNamePattern.MatchString(namespace) {
			return fmt.Errorf("invalid plugin namespace %q", namespace)
		}
		cwd, home, _, err := directories(deps)
		if err != nil {
			return err
		}
		root := filepath.Join(cwd, ".wuko", "plugins")
		if global {
			root = filepath.Join(home, ".wuko", "plugins")
		}
		directory := filepath.Join(root, namespace)
		if _, err := pluginpkg.ValidateInstallation(directory, namespace); err != nil {
			return err
		}
		if !yes {
			if !deps.isInteractive(command.InOrStdin()) {
				return fmt.Errorf("refusing non-interactive uninstall without --yes")
			}
			confirmed, err := deps.confirm(command.Context(), command.InOrStdin(), command.OutOrStdout(), fmt.Sprintf("Uninstall plugin %s?", namespace), false)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		}
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("removing plugin %s: %w", namespace, err)
		}
		fmt.Fprintf(command.OutOrStdout(), "Uninstalled plugin %s\n", namespace)
		return nil
	}}
	command.Flags().BoolVar(&global, "global", false, "use the user plugin directory")
	command.Flags().BoolVar(&yes, "yes", false, "confirm uninstall non-interactively")
	return command
}

func newPluginInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init NAMESPACE [DIRECTORY]", Short: "Create a standalone Go executable plugin", Args: cobra.RangeArgs(1, 2), RunE: func(command *cobra.Command, args []string) error {
		namespace := args[0]
		if !pluginNamePattern.MatchString(namespace) {
			return fmt.Errorf("invalid plugin namespace %q", namespace)
		}
		directory := "wuko-plugin-" + namespace
		if len(args) == 2 {
			directory = args[1]
		}
		if !filepath.IsAbs(directory) {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			directory = filepath.Join(cwd, directory)
		}
		if err := writeGoPluginScaffold(directory, namespace); err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Initialized Go plugin %s in %s\n", namespace, directory)
		return nil
	}}
}

func writeGoPluginScaffold(directory, namespace string) error {
	if entries, err := os.ReadDir(directory); err == nil && len(entries) > 0 {
		return fmt.Errorf("directory %s is not empty", directory)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(directory, "examples"), 0755); err != nil {
		return err
	}
	files := scaffoldFiles()
	for name, content := range files {
		content = strings.ReplaceAll(content, "{{NS}}", namespace)
		target := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func scaffoldFiles() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/wuko-plugin-{{NS}}\n\ngo 1.26\n",
		"main.go": `package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

type request struct { ID string ` + "`json:\"id\"`" + `; Method string ` + "`json:\"method\"`" + `; Params json.RawMessage ` + "`json:\"params\"`" + ` }
type response struct { ID string ` + "`json:\"id\"`" + `; Result any ` + "`json:\"result,omitempty\"`" + `; Error *rpcError ` + "`json:\"error,omitempty\"`" + ` }
type rpcError struct { Message string ` + "`json:\"message\"`" + ` }

func main(){scanner:=bufio.NewScanner(os.Stdin);scanner.Buffer(make([]byte,64<<10),10<<20);encoder:=json.NewEncoder(os.Stdout);for scanner.Scan(){var req request;if err:=json.Unmarshal(scanner.Bytes(),&req);err!=nil{fmt.Fprintln(os.Stderr,err);return};if req.ID==""&&req.Method=="cancel"{continue};result,err:=dispatch(context.Background(),req,encoder);reply:=response{ID:req.ID,Result:result};if err!=nil{reply.Result=nil;reply.Error=&rpcError{Message:err.Error()}};if err:=encoder.Encode(reply);err!=nil{return};if req.Method=="shutdown"{return}}}

func dispatch(ctx context.Context,req request,encoder *json.Encoder)(any,error){switch req.Method{case "initialize":return map[string]any{"protocol":protocolVersion,"namespace":"{{NS}}","lifecycle":true,"steps":[]any{map[string]any{"type":"{{NS}}.uppercase"}},"executors":[]any{map[string]any{"type":"{{NS}}.local","cancel_stops_process":true}}},nil;case "plugin.start":var p struct{With map[string]any ` + "`json:\"with\"`" + `};_ = json.Unmarshal(req.Params,&p);return map[string]any{},Start(ctx,p.With);case "plugin.stop":var p struct{Reason string ` + "`json:\"reason\"`" + `};_ = json.Unmarshal(req.Params,&p);return map[string]any{},Stop(ctx,p.Reason);case "step.validate","executor.validate","executor.close":return map[string]any{},nil;case "step.run":return runUppercase(req.Params);case "executor.open":return map[string]any{"session":"local"},nil;case "executor.run":return runLocal(ctx,req,encoder);case "shutdown":return map[string]any{},nil;default:return nil,fmt.Errorf("unknown method %s",req.Method)}}
func event(encoder *json.Encoder,id,kind string,data []byte){_ = encoder.Encode(map[string]any{"id":id,"event":kind,"data":base64.StdEncoding.EncodeToString(data)})}
`,
		"protocol.go": `package main

// protocolVersion is the JSONL contract implemented by this executable.
const protocolVersion = "wuko.plugin/v1"
`,
		"lifecycle.go": `package main
import "context"
// Start is called once before the first runtime operation. Configuration is plugins.{{NS}}.with.
func Start(context.Context,map[string]any)error{return nil}
// Stop is called once after executor close and step cleanup. Reason is completed, failed, or canceled.
func Stop(context.Context,string)error{return nil}
`,
		"uppercase.go": `package main
import("encoding/json";"fmt";"strings")
func runUppercase(raw json.RawMessage)(any,error){var p struct{With struct{Value string ` + "`json:\"value\"`" + `} ` + "`json:\"with\"`" + `};if err:=json.Unmarshal(raw,&p);err!=nil{return nil,err};if p.With.Value==""{return nil,fmt.Errorf("value is required")};return map[string]any{"outputs":map[string]any{"value":strings.ToUpper(p.With.Value)}},nil}
`,
		"local_executor.go": `package main
import("bytes";"context";"encoding/base64";"encoding/json";"os";"os/exec")
func runLocal(ctx context.Context,req request,encoder *json.Encoder)(any,error){var p struct{Command string ` + "`json:\"command\"`" + `;Args []string ` + "`json:\"args\"`" + `;Dir string ` + "`json:\"dir\"`" + `;Env map[string]string ` + "`json:\"env\"`" + `;Stdin string ` + "`json:\"stdin\"`" + `};if err:=json.Unmarshal(req.Params,&p);err!=nil{return nil,err};cmd:=exec.CommandContext(ctx,p.Command,p.Args...);cmd.Dir=p.Dir;cmd.Env=os.Environ();for k,v:=range p.Env{cmd.Env=append(cmd.Env,k+"="+v)};input,_:=base64.StdEncoding.DecodeString(p.Stdin);cmd.Stdin=bytes.NewReader(input);var stdout,stderr bytes.Buffer;cmd.Stdout=&stdout;cmd.Stderr=&stderr;event(encoder,req.ID,"started",nil);err:=cmd.Run();event(encoder,req.ID,"stdout",stdout.Bytes());event(encoder,req.ID,"stderr",stderr.Bytes());code:=0;if cmd.ProcessState!=nil{code=cmd.ProcessState.ExitCode();err=nil};return map[string]any{"stdout":stdout.String(),"stderr":stderr.String(),"exit_code":code},err}
`,
		"plugin_test.go": `package main
import("encoding/json";"testing")
func TestUppercase(t *testing.T){result,err:=runUppercase(json.RawMessage(` + "`{\"with\":{\"value\":\"hello\"}}`" + `));if err!=nil{t.Fatal(err)};if result.(map[string]any)["outputs"].(map[string]any)["value"]!="HELLO"{t.Fatal(result)}}
`,
		"justfile": `build:
	go build -o wuko-plugin-{{NS}} .
test:
	go test ./...
vet:
	go vet ./...
fmt:
	gofmt -w *.go
check: fmt vet test
install: build
	mkdir -p "${HOME}/.wuko/plugins/{{NS}}"
	cp wuko-plugin-{{NS}} "${HOME}/.wuko/plugins/{{NS}}/"
`,
		".gitignore": "/wuko-plugin-{{NS}}\n/dist/\n",
		"examples/workflow.yaml": `version: 1
name: {{NS}}-example
plugins:
  {{NS}}:
    source: github:OWNER/wuko-plugin-{{NS}}@PINNED_REF
    sha256: REPLACE_WITH_64_CHARACTER_MANIFEST_SHA256
    with:
      example: value
steps:
  - id: uppercase
    type: {{NS}}.uppercase
    with:
      value: hello
`,
		"README.md": `# wuko-plugin-{{NS}}

A standard-library-only Wuko JSONL plugin. Run **just check** and **just build**.

The executable keeps stdin/stdout exclusively for protocol frames; diagnostics go to stderr. Start and Stop are command-wide hooks. Edit lifecycle.go to use values from plugins.{{NS}}.with.

Publish tar.gz archives whose executable entry is named wuko-plugin-{{NS}}, then replace the example digests in plugin.json. Wuko verifies both the manifest and selected archive before launch.
`,
		"plugin.json": `{
  "version": 1,
  "namespace": "{{NS}}",
  "plugin_version": "0.1.0",
  "protocol": "wuko.plugin/v1",
  "artifacts": [
    {"os":"darwin","arch":"amd64","path":"dist/wuko-plugin-{{NS}}_Darwin_amd64.tar.gz","format":"tar.gz","entry":"wuko-plugin-{{NS}}","sha256":"REPLACE_WITH_64_CHARACTER_SHA256"},
    {"os":"darwin","arch":"arm64","path":"dist/wuko-plugin-{{NS}}_Darwin_arm64.tar.gz","format":"tar.gz","entry":"wuko-plugin-{{NS}}","sha256":"REPLACE_WITH_64_CHARACTER_SHA256"},
    {"os":"linux","arch":"amd64","path":"dist/wuko-plugin-{{NS}}_Linux_amd64.tar.gz","format":"tar.gz","entry":"wuko-plugin-{{NS}}","sha256":"REPLACE_WITH_64_CHARACTER_SHA256"},
    {"os":"linux","arch":"arm64","path":"dist/wuko-plugin-{{NS}}_Linux_arm64.tar.gz","format":"tar.gz","entry":"wuko-plugin-{{NS}}","sha256":"REPLACE_WITH_64_CHARACTER_SHA256"}
  ]
}
`}
}
