package githook

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func Context(repository Repository, name string, args []string, stdin string) (map[string]any, error) {
	payload, err := parsePayload(repository.Root, name, args, stdin)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"repository": map[string]any{"root": repository.Root, "git_dir": repository.GitDir, "common_dir": repository.CommonDir},
		"hook":       map[string]any{"name": name, "args": append([]string(nil), args...), "stdin": stdin, "payload": payload},
	}, nil
}

func parsePayload(root, name string, args []string, stdin string) (map[string]any, error) {
	payload := make(map[string]any)
	require := func(minimum int) error {
		if len(args) < minimum {
			return fmt.Errorf("Git hook %s requires at least %d argument(s), received %d", name, minimum, len(args))
		}
		return nil
	}
	absPath := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(root, path)
	}
	switch name {
	case "applypatch-msg", "commit-msg":
		if err := require(1); err != nil {
			return nil, err
		}
		payload["message_file"] = absPath(args[0])
	case "sendemail-validate":
		if err := require(1); err != nil {
			return nil, err
		}
		payload["patch_file"] = absPath(args[0])
	case "prepare-commit-msg":
		if err := require(1); err != nil {
			return nil, err
		}
		payload["message_file"] = absPath(args[0])
		if len(args) > 1 {
			payload["source"] = args[1]
		}
		if len(args) > 2 {
			payload["commit_oid"] = args[2]
		}
	case "pre-rebase":
		if err := require(1); err != nil {
			return nil, err
		}
		payload["upstream"] = args[0]
		if len(args) > 1 {
			payload["branch"] = args[1]
		}
	case "post-checkout":
		if err := require(3); err != nil {
			return nil, err
		}
		branchCheckout, err := gitBool(args[2])
		if err != nil {
			return nil, fmt.Errorf("Git hook %s checkout flag: %w", name, err)
		}
		payload["previous_oid"], payload["new_oid"] = args[0], args[1]
		payload["branch_checkout"] = branchCheckout
	case "post-merge":
		if err := require(1); err != nil {
			return nil, err
		}
		squash, err := gitBool(args[0])
		if err != nil {
			return nil, fmt.Errorf("Git hook %s squash flag: %w", name, err)
		}
		payload["squash"] = squash
	case "pre-push":
		if err := require(2); err != nil {
			return nil, err
		}
		payload["remote_name"], payload["remote_url"] = args[0], args[1]
		updates, err := parseRows(name, stdin, 4, []string{"local_ref", "local_oid", "remote_ref", "remote_oid"})
		if err != nil {
			return nil, err
		}
		payload["updates"] = updates
	case "post-rewrite":
		if err := require(1); err != nil {
			return nil, err
		}
		payload["command"] = args[0]
		rewrites, err := parseRows(name, stdin, 2, []string{"old_oid", "new_oid"})
		if err != nil {
			return nil, err
		}
		payload["rewrites"] = rewrites
	case "post-index-change":
		if err := require(2); err != nil {
			return nil, err
		}
		working, err := gitBool(args[0])
		if err != nil {
			return nil, fmt.Errorf("Git hook %s working-tree flag: %w", name, err)
		}
		skip, err := gitBool(args[1])
		if err != nil {
			return nil, fmt.Errorf("Git hook %s skip-worktree flag: %w", name, err)
		}
		payload["working_tree_updated"], payload["skip_worktree_updated"] = working, skip
	}
	return payload, nil
}

func parseRows(name, input string, width int, keys []string) ([]any, error) {
	var rows []any
	for index, line := range strings.Split(strings.TrimSuffix(input, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != width {
			return nil, fmt.Errorf("Git hook %s stdin line %d contains %d fields, expected %d", name, index+1, len(fields), width)
		}
		row := make(map[string]any, width)
		for i, key := range keys {
			row[key] = fields[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func gitBool(value string) (bool, error) {
	number, err := strconv.Atoi(value)
	if err != nil || (number != 0 && number != 1) {
		return false, fmt.Errorf("expected 0 or 1, received %q", value)
	}
	return number == 1, nil
}
