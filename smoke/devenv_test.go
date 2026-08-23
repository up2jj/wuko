//go:build devenv_smoke

package smoke

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDevenvIntegration(t *testing.T) {
	root := filepath.Join("..", "scripts", "smoke-devenv.sh")
	command := exec.Command("bash", root)
	output, err := command.CombinedOutput()
	if len(output) > 0 {
		t.Log(string(output))
	}
	if err != nil {
		t.Fatalf("devenv smoke test failed: %v", err)
	}
}
