package scriptsdk

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bash.bash
var bashBootstrap string

// InjectBash installs functions through BASH_ENV. executable must be the host
// Wuko binary; all transport and JSON processing runs in that existing binary.
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
	bootstrap := bashBootstrap
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
