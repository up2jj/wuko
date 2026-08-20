// Package agent discovers supported coding-agent CLIs and installs Wuko skills
// into their user-level skill directories.
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Definition describes a supported coding agent that can consume Wuko skills.
type Definition struct {
	Name           string
	Command        string
	Executable     string
	SkillDirectory string
}

type candidate struct {
	name    string
	command string
	dir     func(string) string
}

var candidates = []candidate{
	{name: "claude", command: "claude", dir: func(home string) string { return filepath.Join(home, ".claude", "skills") }},
	{name: "codex", command: "codex", dir: func(home string) string { return filepath.Join(home, ".agents", "skills") }},
}

// Discover returns supported agents whose command is available on PATH.
// Results are returned in stable command order.
func Discover(home string, lookPath func(string) (string, error)) []Definition {
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	discovered := make([]Definition, 0, len(candidates))
	for _, candidate := range candidates {
		executable, err := lookPath(candidate.command)
		if err != nil {
			continue
		}
		discovered = append(discovered, Definition{
			Name:           candidate.name,
			Command:        candidate.command,
			Executable:     executable,
			SkillDirectory: candidate.dir(home),
		})
	}
	return discovered
}

// Find returns a discovered agent by its user-facing name.
func Find(agents []Definition, name string) (Definition, bool) {
	for _, agent := range agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return Definition{}, false
}

// Install copies every embedded Wuko skill into destination. Existing files
// are replaced atomically, making repeated installation idempotent.
func Install(source fs.FS, destination string) ([]string, error) {
	if source == nil {
		return nil, fmt.Errorf("skill source is nil")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, fmt.Errorf("creating skill directory %s: %w", destination, err)
	}

	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("reading skill source: %w", err)
	}

	installed := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := path.Join(entry.Name(), "SKILL.md")
		if _, err := fs.Stat(source, root); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("checking skill %s: %w", entry.Name(), err)
		}
		if err := copySkill(source, entry.Name(), destination); err != nil {
			return nil, fmt.Errorf("installing skill %s: %w", entry.Name(), err)
		}
		installed = append(installed, entry.Name())
	}
	if len(installed) == 0 {
		return nil, fmt.Errorf("skill source contains no skills")
	}
	return installed, nil
}

func copySkill(source fs.FS, skillName, destination string) error {
	return fs.WalkDir(source, skillName, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		clean := path.Clean(sourcePath)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("unsafe skill path %q", sourcePath)
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating parent directory: %w", err)
		}
		data, err := fs.ReadFile(source, sourcePath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", sourcePath, err)
		}
		return atomicWrite(target, data)
	})
}

func atomicWrite(target string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".wuko-skill-*")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("setting file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}
