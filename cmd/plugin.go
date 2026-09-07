package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func newPluginCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Install and manage executable plugins"}
	command.AddCommand(newPluginInstallCmd(deps), newPluginUninstallCmd(deps))
	return command
}

func newPluginInstallCmd(deps dependencies) *cobra.Command {
	var global, reinstall bool
	var packages []string
	command := &cobra.Command{Use: "install SOURCE", Short: "Install a plugin release or selected marketplace plugins", Example: "  wuko plugin install --package acme https://github.com/acme/wuko-marketplace\n  wuko plugin install --global --reinstall --package acme https://github.com/acme/wuko-marketplace", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return installPlugin(command, deps, args[0], global, reinstall, packages)
	}}
	command.Flags().BoolVar(&global, "global", false, "install in the user plugin directory")
	command.Flags().BoolVar(&reinstall, "reinstall", false, "replace an existing installation")
	command.Flags().StringArrayVar(&packages, "package", nil, "select a marketplace plugin namespace (repeatable)")
	return command
}

func installPlugin(command *cobra.Command, deps dependencies, source string, global, reinstall bool, requested []string) error {
	cwd, home, _, err := directories(deps)
	if err != nil {
		return err
	}
	root := filepath.Join(cwd, ".wuko", "plugins")
	if global {
		root = filepath.Join(home, ".wuko", "plugins")
	}
	if isHTTPSURL(source) {
		manifest, manifestErr := deps.loader.DiscoverMarketplace(command.Context(), source)
		if manifestErr == nil {
			return installPluginMarketplace(command, deps, source, root, reinstall, requested, manifest)
		}
		if !errors.Is(manifestErr, workflow.ErrMarketplaceNotFound) {
			return manifestErr
		}
	}
	if len(requested) > 0 {
		return fmt.Errorf("--package can only be used when SOURCE is a marketplace")
	}
	return installPluginRelease(command, deps, source, "", "", "", root, reinstall)
}

func installPluginMarketplace(command *cobra.Command, deps dependencies, source, root string, reinstall bool, requested []string, manifest workflow.MarketplaceManifest) error {
	if len(manifest.Plugins) == 0 {
		return fmt.Errorf("marketplace %s contains no plugins", source)
	}
	selected, err := selectMarketplacePlugins(command, deps, manifest.Plugins, requested)
	if err != nil || selected == nil {
		return err
	}
	for index, item := range manifest.Plugins {
		if _, ok := selected[index]; !ok {
			continue
		}
		resolved, err := workflow.ResolveMarketplacePlugin(source, item)
		if err != nil {
			return fmt.Errorf("resolving marketplace plugin %q: %w", item.Namespace, err)
		}
		if err := installPluginRelease(command, deps, resolved, item.SHA256, item.Namespace, item.PluginVersion, root, reinstall); err != nil {
			return fmt.Errorf("installing marketplace plugin %q: %w", item.Namespace, err)
		}
	}
	return nil
}

func installPluginRelease(command *cobra.Command, deps dependencies, source, expectedDigest, expectedNamespace, expectedVersion, root string, reinstall bool) error {
	var marker pluginpkg.InstallationMarker
	var err error
	if expectedNamespace == "" {
		marker, err = pluginpkg.InstallPinned(command.Context(), source, expectedDigest, root, reinstall, deps.httpClient, command.ErrOrStderr())
	} else {
		marker, err = pluginpkg.InstallMarketplace(command.Context(), source, expectedDigest, expectedNamespace, expectedVersion, root, reinstall, deps.httpClient, command.ErrOrStderr())
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Installed plugin %s %s in %s\n", marker.Namespace, marker.PluginVersion, filepath.Join(root, marker.Namespace))
	return err
}

func selectMarketplacePlugins(command *cobra.Command, deps dependencies, plugins []workflow.MarketplacePluginPackage, requested []string) (map[int]struct{}, error) {
	if len(requested) > 0 {
		indexes := make(map[string]int, len(plugins))
		for index, item := range plugins {
			indexes[item.Namespace] = index
		}
		selected := make(map[int]struct{}, len(requested))
		for _, namespace := range requested {
			index, ok := indexes[namespace]
			if !ok {
				return nil, fmt.Errorf("marketplace plugin %q was not found", namespace)
			}
			if !marketplacePluginSupportsCurrentPlatform(plugins[index]) {
				return nil, fmt.Errorf("marketplace plugin %q does not support %s/%s", namespace, runtime.GOOS, runtime.GOARCH)
			}
			if _, exists := selected[index]; exists {
				return nil, fmt.Errorf("marketplace plugin %q was selected more than once", namespace)
			}
			selected[index] = struct{}{}
		}
		return selected, nil
	}
	if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
		return nil, fmt.Errorf("marketplace plugin install requires an interactive terminal or at least one --package flag")
	}
	var options []tui.Option
	var indexes []int
	for index, item := range plugins {
		if !marketplacePluginSupportsCurrentPlatform(item) {
			continue
		}
		options = append(options, tui.Option{Label: item.Namespace, Description: marketplacePluginDescription(item), Path: item.Path, Value: item})
		indexes = append(indexes, index)
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("marketplace contains no plugins for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	selectMany := deps.selectMany
	if selectMany == nil {
		selectMany = tui.SelectMany
	}
	chosen, err := selectMany(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Marketplace plugins", options)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil
		}
		return nil, fmt.Errorf("selecting marketplace plugins: %w", err)
	}
	selected := make(map[int]struct{}, len(chosen))
	for _, optionIndex := range chosen {
		if optionIndex < 0 || optionIndex >= len(indexes) {
			return nil, fmt.Errorf("marketplace picker returned invalid plugin index %d", optionIndex)
		}
		selected[indexes[optionIndex]] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("select at least one marketplace plugin")
	}
	return selected, nil
}

func marketplacePluginSupportsCurrentPlatform(item workflow.MarketplacePluginPackage) bool {
	return slices.ContainsFunc(item.Platforms, func(platform workflow.MarketplacePlatform) bool {
		return platform.OS == runtime.GOOS && platform.Arch == runtime.GOARCH
	})
}

func marketplacePluginDescription(item workflow.MarketplacePluginPackage) string {
	version := "plugin " + item.PluginVersion
	if item.Description == "" {
		return version
	}
	return item.Description + " • " + version
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
	return &cobra.Command{Use: "init NAMESPACE [DIRECTORY]", Short: "Create a standalone Go executable plugin", Example: "  wuko marketplace plugin init hello\n  wuko marketplace plugin init hello ./plugins/hello", Args: cobra.RangeArgs(1, 2), RunE: func(command *cobra.Command, args []string) error {
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

func dispatch(ctx context.Context,req request,encoder *json.Encoder)(any,error){switch req.Method{case "initialize":return map[string]any{"protocol":protocolVersion,"namespace":"{{NS}}","lifecycle":true,"helpers":[]any{map[string]any{"name":"slug"}},"steps":[]any{map[string]any{"type":"{{NS}}.uppercase"}},"executors":[]any{map[string]any{"type":"{{NS}}.local","cancel_stops_process":true}}},nil;case "plugin.start":var p struct{With map[string]any ` + "`json:\"with\"`" + `};_ = json.Unmarshal(req.Params,&p);return map[string]any{},Start(ctx,p.With);case "plugin.stop":var p struct{Reason string ` + "`json:\"reason\"`" + `};_ = json.Unmarshal(req.Params,&p);return map[string]any{},Stop(ctx,p.Reason);case "helper.call":return callHelper(req.Params);case "step.validate","executor.validate","executor.close":return map[string]any{},nil;case "step.run":return runUppercase(req.Params);case "executor.open":return map[string]any{"session":"local"},nil;case "executor.run":return runLocal(ctx,req,encoder);case "shutdown":return map[string]any{},nil;default:return nil,fmt.Errorf("unknown method %s",req.Method)}}
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
		"helpers.go": `package main
import("encoding/json";"fmt";"regexp";"strings")
var nonSlug=regexp.MustCompile(` + "`[^a-z0-9]+`" + `)
func callHelper(raw json.RawMessage)(any,error){var p struct{Name string ` + "`json:\"name\"`" + `;Args []any ` + "`json:\"args\"`" + `};if err:=json.Unmarshal(raw,&p);err!=nil{return nil,err};if p.Name!="slug"{return nil,fmt.Errorf("unknown helper %q",p.Name)};if len(p.Args)!=1{return nil,fmt.Errorf("slug requires one argument")};value,ok:=p.Args[0].(string);if !ok{return nil,fmt.Errorf("slug argument must be a string")};value=strings.ToLower(strings.TrimSpace(value));return map[string]any{"value":strings.Trim(nonSlug.ReplaceAllString(value,"-"),"-")},nil}
`,
		"local_executor.go": `package main
import("bytes";"context";"encoding/base64";"encoding/json";"os";"os/exec")
func runLocal(ctx context.Context,req request,encoder *json.Encoder)(any,error){var p struct{Command string ` + "`json:\"command\"`" + `;Args []string ` + "`json:\"args\"`" + `;Dir string ` + "`json:\"dir\"`" + `;Env map[string]string ` + "`json:\"env\"`" + `;Stdin string ` + "`json:\"stdin\"`" + `};if err:=json.Unmarshal(req.Params,&p);err!=nil{return nil,err};cmd:=exec.CommandContext(ctx,p.Command,p.Args...);cmd.Dir=p.Dir;cmd.Env=os.Environ();for k,v:=range p.Env{cmd.Env=append(cmd.Env,k+"="+v)};input,_:=base64.StdEncoding.DecodeString(p.Stdin);cmd.Stdin=bytes.NewReader(input);var stdout,stderr bytes.Buffer;cmd.Stdout=&stdout;cmd.Stderr=&stderr;event(encoder,req.ID,"started",nil);err:=cmd.Run();event(encoder,req.ID,"stdout",stdout.Bytes());event(encoder,req.ID,"stderr",stderr.Bytes());code:=0;if cmd.ProcessState!=nil{code=cmd.ProcessState.ExitCode();err=nil};return map[string]any{"stdout":stdout.String(),"stderr":stderr.String(),"exit_code":code},err}
`,
		"plugin_test.go": `package main
import("encoding/json";"testing")
func TestUppercase(t *testing.T){result,err:=runUppercase(json.RawMessage(` + "`{\"with\":{\"value\":\"hello\"}}`" + `));if err!=nil{t.Fatal(err)};if result.(map[string]any)["outputs"].(map[string]any)["value"]!="HELLO"{t.Fatal(result)}}
func TestSlug(t *testing.T){result,err:=callHelper(json.RawMessage(` + "`{\"name\":\"slug\",\"args\":[\"Hello World\"]}`" + `));if err!=nil{t.Fatal(err)};if result.(map[string]any)["value"]!="hello-world"{t.Fatal(result)}}
`,
		"justfile": `build:
	go build -o wuko-plugin-{{NS}} .
test:
	go test ./...
vet:
	go vet ./...
fmt:
	gofmt -w *.go tools/release/*.go
check: fmt vet test
release version:
	go run ./tools/release -version "{{version}}"
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
  - id: slug
    type: set
    with:
      variable: slug
      expr: {{NS}}_slug("Hello World")
  - id: uppercase
    type: {{NS}}.uppercase
    with:
      value: hello
`,
		"README.md": `# wuko-plugin-{{NS}}

A standard-library-only Wuko JSONL plugin with a shared ` + "`{{NS}}_slug`" + ` helper. Run **just check** and **just build**.

The executable keeps stdin/stdout exclusively for protocol frames; diagnostics go to stderr. Start and Stop are command-wide hooks. Edit lifecycle.go to use values from plugins.{{NS}}.with.

The runtime contract is language-neutral. See [Wuko plugin protocol v1](https://github.com/up2jj/wuko/blob/main/docs/plugin-protocol.md) for message envelopes, methods, events, cancellation, and lifecycle ordering.

Run ` + "`just release 0.1.0`" + ` to cross-compile deterministic Darwin/Linux archives for amd64/arm64 and generate plugin.json. From a marketplace repository, import this release with ` + "`wuko marketplace plugin add ../wuko-plugin-{{NS}}`" + `. Wuko verifies both the manifest and selected archive before launch.
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
`,
		"tools/release/main.go": `package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const namespace = "{{NS}}"

type artifact struct { OS string ` + "`json:\"os\"`" + `; Arch string ` + "`json:\"arch\"`" + `; Path string ` + "`json:\"path\"`" + `; Format string ` + "`json:\"format\"`" + `; Entry string ` + "`json:\"entry\"`" + `; SHA256 string ` + "`json:\"sha256\"`" + ` }
type manifest struct { Version int ` + "`json:\"version\"`" + `; Namespace string ` + "`json:\"namespace\"`" + `; PluginVersion string ` + "`json:\"plugin_version\"`" + `; Protocol string ` + "`json:\"protocol\"`" + `; Artifacts []artifact ` + "`json:\"artifacts\"`" + ` }
type target struct { os, arch, label string }

func main() {
	version := flag.String("version", "", "plugin version")
	flag.Parse()
	if strings.TrimSpace(*version) == "" || strings.TrimSpace(*version) != *version { fatal(fmt.Errorf("version must be non-empty without surrounding whitespace")) }
	cwd, err := os.Getwd(); if err != nil { fatal(err) }
	stage, err := os.MkdirTemp(cwd, ".release-*"); if err != nil { fatal(err) }; defer os.RemoveAll(stage)
	targets := []target{{"darwin","amd64","Darwin"},{"darwin","arm64","Darwin"},{"linux","amd64","Linux"},{"linux","arm64","Linux"}}
	result := manifest{Version:1, Namespace:namespace, PluginVersion:*version, Protocol:"wuko.plugin/v1"}
	for _, item := range targets {
		binary := filepath.Join(stage, "bin", item.os+"-"+item.arch, "wuko-plugin-"+namespace)
		if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil { fatal(err) }
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+item.os, "GOARCH="+item.arch)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil { fatal(fmt.Errorf("building %s/%s: %w", item.os, item.arch, err)) }
		name := "wuko-plugin-"+namespace+"_"+item.label+"_"+item.arch+".tar.gz"
		relative := filepath.ToSlash(filepath.Join("dist", name)); archivePath := filepath.Join(stage, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil { fatal(err) }
		if err := writeArchive(binary, archivePath); err != nil { fatal(err) }
		data, err := os.ReadFile(archivePath); if err != nil { fatal(err) }; sum := sha256.Sum256(data)
		result.Artifacts = append(result.Artifacts, artifact{OS:item.os, Arch:item.arch, Path:relative, Format:"tar.gz", Entry:"wuko-plugin-"+namespace, SHA256:hex.EncodeToString(sum[:])})
	}
	manifestPath := filepath.Join(stage, "plugin.json"); file, err := os.Create(manifestPath); if err != nil { fatal(err) }
	encoder := json.NewEncoder(file); encoder.SetIndent("", "  "); if err := encoder.Encode(result); err != nil { file.Close(); fatal(err) }; if err := file.Close(); err != nil { fatal(err) }
	if err := replace(filepath.Join(cwd,"dist"), filepath.Join(stage,"dist")); err != nil { fatal(err) }
	if err := replace(filepath.Join(cwd,"plugin.json"), manifestPath); err != nil { fatal(err) }
	fmt.Printf("released plugin %s %s for %d platforms\n", namespace, *version, len(targets))
}

func writeArchive(binary, destination string) error {
	input, err := os.Open(binary); if err != nil { return err }; defer input.Close()
	info, err := input.Stat(); if err != nil { return err }
	output, err := os.Create(destination); if err != nil { return err }
	gzipWriter := gzip.NewWriter(output); gzipWriter.Header.ModTime = time.Unix(0,0); gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name:"wuko-plugin-"+namespace, Mode:0755, Size:info.Size(), ModTime:time.Unix(0,0), AccessTime:time.Unix(0,0), ChangeTime:time.Unix(0,0), Format:tar.FormatPAX}
	if err := tarWriter.WriteHeader(header); err != nil { output.Close(); return err }
	if _, err := io.Copy(tarWriter, input); err != nil { output.Close(); return err }
	if err := tarWriter.Close(); err != nil { output.Close(); return err }; if err := gzipWriter.Close(); err != nil { output.Close(); return err }; return output.Close()
}

func replace(target, staged string) error { if err := os.RemoveAll(target); err != nil { return err }; return os.Rename(staged, target) }
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
`,
	}
}
