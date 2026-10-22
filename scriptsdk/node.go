package scriptsdk

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed node.cjs
var nodePreload []byte

// InjectNode adds the Node adapter to a step-local environment. The descriptor
// and bridge protocol remain usable by other language adapters.
func (b *Bridge) InjectNode(environment map[string]string) error {
	descriptor, err := b.writeDescriptor()
	if err != nil {
		return fmt.Errorf("writing script SDK descriptor: %w", err)
	}
	preload := filepath.Join(b.dir, "node.cjs")
	if err := os.WriteFile(preload, nodePreload, 0o600); err != nil {
		return fmt.Errorf("writing Node SDK preload: %w", err)
	}
	// NODE_OPTIONS uses its own double-quoted token syntax, not shell quoting.
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(preload)
	options := `--require "` + quoted + `"`
	if existing := environment["NODE_OPTIONS"]; existing != "" {
		options += " " + existing
	}
	environment["NODE_OPTIONS"] = options
	environment[DescriptorEnvironment] = descriptor
	return nil
}
