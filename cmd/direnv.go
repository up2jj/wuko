package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func direnvEnvironment(ctx context.Context, dir string) (map[string]string, error) {
	environment := processEnvironment()
	executable, err := exec.LookPath("direnv")
	if errors.Is(err, exec.ErrNotFound) {
		return environment, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding direnv: %w", err)
	}

	command := exec.CommandContext(ctx, executable, "export", "json")
	command.Dir = dir
	command.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("loading direnv environment: %w", ctxErr)
		}
		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			return nil, fmt.Errorf("loading direnv environment: %s", diagnostic)
		}
		return nil, fmt.Errorf("loading direnv environment: %w", err)
	}
	if err := applyDirenvExport(environment, stdout.Bytes()); err != nil {
		return nil, fmt.Errorf("loading direnv environment: %w", err)
	}
	return environment, nil
}

func applyDirenvExport(environment map[string]string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	changes := make(map[string]*string)
	if err := json.Unmarshal(data, &changes); err != nil {
		return fmt.Errorf("decoding export: %w", err)
	}
	for name, value := range changes {
		if value == nil {
			delete(environment, name)
			continue
		}
		environment[name] = *value
	}
	return nil
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
