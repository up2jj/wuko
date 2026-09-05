package githook

import (
	"fmt"
	"os"
	"path/filepath"
)

const starterManifest = `version: 1
hooks:
  pre-commit:
    - workflow: git-check
      target: staged
  commit-msg:
    - workflow: git-commit-message
  pre-push:
    - workflow: git-check
      target: pushed
`

const starterCheckWorkflow = `version: 1
name: git-check
description: Check changes handled by Git hooks

targets:
  staged:
    steps:
      - id: whitespace
        type: shell
        with:
          command: git
          args: [diff, --cached, --check]

  pushed:
    steps:
      - id: whitespace
        type: shell
        with:
          script: |
            set -eu
            zero=0000000000000000000000000000000000000000
            while read -r local_ref local_oid remote_ref remote_oid; do
              [ -n "${local_oid:-}" ] || continue
              [ "$local_oid" = "$zero" ] && continue
              if [ "$remote_oid" = "$zero" ]; then
                commits=$(git rev-list "$local_oid" --not --remotes)
              else
                commits=$(git rev-list "$remote_oid..$local_oid")
              fi
              for commit in $commits; do
                git diff-tree --check --root -r "$commit"
              done
            done
          stdin: "{{ .git.hook.stdin }}"
`

const starterCommitMessageWorkflow = `version: 1
name: git-commit-message
description: Validate a Conventional Commit message

steps:
  - id: read_message
    type: file
    with:
      operation: read
      path: "{{ .git.hook.payload.message_file }}"

  - id: conventional
    type: git_conventional_commit
    with:
      operation: validate
      message: "{{ .steps.read_message.content }}"
`

type scaffoldFile struct {
	relative string
	data     string
}

var starterFiles = []scaffoldFile{
	{relative: ManifestPath, data: starterManifest},
	{relative: ".wuko/workflows/git-check.yaml", data: starterCheckWorkflow},
	{relative: ".wuko/workflows/git-commit-message.yaml", data: starterCommitMessageWorkflow},
}

// Scaffold creates a starter Git hook manifest and its referenced workflows. Existing files are
// never overwritten, and every collision is checked before any file is created.
func Scaffold(repository Repository) ([]string, error) {
	paths := make([]string, len(starterFiles))
	for index, file := range starterFiles {
		path := filepath.Join(repository.Root, filepath.FromSlash(file.relative))
		paths[index] = path
		if _, err := os.Lstat(path); err == nil {
			return nil, fmt.Errorf("Git hook scaffold file already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("checking Git hook scaffold path %s: %w", path, err)
		}
	}

	workflowDir := filepath.Join(repository.Root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating Git hook workflow directory: %w", err)
	}
	created := make([]string, 0, len(starterFiles))
	for index, file := range starterFiles {
		path := paths[index]
		if err := createScaffoldFile(path, []byte(file.data)); err != nil {
			for _, createdPath := range created {
				_ = os.Remove(createdPath)
			}
			return nil, err
		}
		created = append(created, path)
	}
	return paths, nil
}

func createScaffoldFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("creating Git hook scaffold file %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("writing Git hook scaffold file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("closing Git hook scaffold file %s: %w", path, err)
	}
	return nil
}
