package cmd

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	inputstep "github.com/up2jj/wuko/steps/input"
	passwordstep "github.com/up2jj/wuko/steps/password"
)

func TestTextVariablesExplainJSONQuotingWithoutExposingValues(t *testing.T) {
	t.Parallel()
	for _, stepType := range []string{"tui_input", "tui_password"} {
		for _, value := range []string{"98473625", "true", "null", `["private-token"]`, `"123"`} {
			t.Run(stepType+"/"+value, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "workflow.yaml")
				data := fmt.Sprintf("version: 1\nname: text\nsteps:\n  - id: input\n    type: %s\n    with: {variable: code, message: Code}\n", stepType)
				testfs.WriteFile(t, path, []byte(data), 0o644)
				var output bytes.Buffer
				deps := listTestDependencies(root, &output)
				if err := inputstep.Register(deps.registry); err != nil {
					t.Fatal(err)
				}
				if err := passwordstep.Register(deps.registry); err != nil {
					t.Fatal(err)
				}
				command := newRootCmd(deps)
				command.SetArgs([]string{"run", "--file", path, "--var", "code=" + value})
				err := command.ExecuteContext(t.Context())
				if value == `"123"` {
					if err != nil {
						t.Fatalf("JSON string failed: %v", err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "must be a string") || !strings.Contains(err.Error(), `--var 'key="123"'`) {
					t.Fatalf("error = %v, want JSON quoting hint", err)
				}
				for _, secret := range []string{"98473625", "private-token"} {
					if strings.Contains(err.Error()+output.String(), secret) {
						t.Fatal("type error exposed a supplied value")
					}
				}
			})
		}
	}
}
