package scriptsdk

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bash.bash
var bashBootstrap string

// InjectBash installs functions through BASH_ENV. executable must be the host
// Wuko binary, used as a fallback for Bash builds without network redirections.
func (b *Bridge) InjectBash(environment map[string]string, executable string) error {
	_, posix := environment["POSIXLY_CORRECT"]
	if posix || strings.Contains(":"+environment["SHELLOPTS"]+":", ":posix:") {
		return fmt.Errorf("Bash SDK does not support POSIX mode enabled through the environment")
	}
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("Bash SDK requires an absolute Wuko executable path")
	}
	descriptor, err := b.writeDescriptor()
	if err != nil {
		return fmt.Errorf("writing script SDK descriptor: %w", err)
	}
	// Without IPv4 loopback, an empty port fails the probe and selects the
	// executable client, which uses the Unix socket.
	port, err := b.openBashListener()
	if err != nil {
		if b.isClosing() {
			return err
		}
		port = ""
	}
	// Read credentials as data so Bash -v cannot disclose them by echoing source.
	tokenPath := filepath.Join(b.dir, "bash-token")
	if err := os.WriteFile(tokenPath, []byte(b.descriptor.Token+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing Bash SDK credential: %w", err)
	}
	bootstrap := fmt.Sprintf("__wuko_sdk_port='%s'\nIFS= read -r __wuko_sdk_token < '%s'\n", port, tokenPath) + bashBootstrap
	if previous := environment["BASH_ENV"]; previous != "" {
		// Treat the prior path as data, including quotes and shell metacharacters.
		quoted := "'" + strings.ReplaceAll(previous, "'", "'\"'\"'") + "'"
		bootstrap += "\nif [[ -f " + quoted + " ]]; then source " + quoted + "; fi\n"
	}
	path := filepath.Join(b.dir, "bootstrap.bash")
	if err := os.WriteFile(path, []byte(bootstrap), 0o600); err != nil {
		return fmt.Errorf("writing Bash SDK bootstrap: %w", err)
	}
	environment["BASH_ENV"] = path
	environment["WUKO_SDK_EXECUTABLE"] = executable
	environment[DescriptorEnvironment] = descriptor
	return nil
}

// Bash builtins cannot open Unix sockets. Serve the same authenticated host on
// an IPv4 loopback listener so calls need no executable or shared client state.
func (b *Bridge) isClosing() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closing
}

func (b *Bridge) openBashListener() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return "", fmt.Errorf("script SDK bridge is closed")
	}
	if b.bashPort != "" {
		return b.bashPort, nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("opening Bash SDK loopback listener: %w", err)
	}
	b.bashPort = fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	b.bashServed = make(chan struct{})
	go func() {
		defer close(b.bashServed)
		_ = b.server.Serve(listener)
	}()
	return b.bashPort, nil
}
