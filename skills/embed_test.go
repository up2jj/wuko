package skills

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	markdownLinkPattern = regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	skillNamePattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

func TestBundledSkillsAreValid(t *testing.T) {
	entries, err := fs.ReadDir(Assets, ".")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "wuko-") {
			continue
		}
		names = append(names, entry.Name())
		validateBundledSkill(t, entry.Name())
	}

	want := []string{
		"wuko-agent-handoff",
		"wuko-workflow-author",
		"wuko-workflow-debugger",
		"wuko-workflow-runner",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("bundled skills = %v, want %v", names, want)
	}
}

func validateBundledSkill(t *testing.T, skillName string) {
	t.Helper()

	skillPath := path.Join(skillName, "SKILL.md")
	content, err := fs.ReadFile(Assets, skillPath)
	if err != nil {
		t.Fatal(err)
	}
	frontmatter := parseSkillFrontmatter(t, skillPath, content)
	if frontmatter.name != skillName {
		t.Errorf("%s name = %q, want %q", skillPath, frontmatter.name, skillName)
	}

	metadataPath := path.Join(skillName, "agents", "openai.yaml")
	metadataContent, err := fs.ReadFile(Assets, metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Interface struct {
			DisplayName      string `yaml:"display_name"`
			ShortDescription string `yaml:"short_description"`
			DefaultPrompt    string `yaml:"default_prompt"`
		} `yaml:"interface"`
	}
	if err := yaml.Unmarshal(metadataContent, &metadata); err != nil {
		t.Fatalf("parsing %s: %v", metadataPath, err)
	}
	if metadata.Interface.DisplayName == "" || metadata.Interface.ShortDescription == "" {
		t.Errorf("%s must define display_name and short_description", metadataPath)
	}
	if !strings.Contains(metadata.Interface.DefaultPrompt, "$"+skillName) {
		t.Errorf("%s default_prompt must invoke $%s", metadataPath, skillName)
	}

	if err := fs.WalkDir(Assets, skillName, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filePath) != ".md" {
			return nil
		}
		data, err := fs.ReadFile(Assets, filePath)
		if err != nil {
			return err
		}
		validateLocalLinks(t, skillName, filePath, string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type skillFrontmatter struct {
	name        string
	description string
}

func parseSkillFrontmatter(t *testing.T, filePath string, content []byte) skillFrontmatter {
	t.Helper()

	body := string(content)
	if !strings.HasPrefix(body, "---\n") {
		t.Fatalf("%s has no YAML frontmatter", filePath)
	}
	frontmatterText, _, ok := strings.Cut(strings.TrimPrefix(body, "---\n"), "\n---\n")
	if !ok {
		t.Fatalf("%s has unterminated YAML frontmatter", filePath)
	}

	var values map[string]any
	if err := yaml.Unmarshal([]byte(frontmatterText), &values); err != nil {
		t.Fatalf("parsing %s frontmatter: %v", filePath, err)
	}
	allowed := map[string]bool{
		"name": true, "description": true, "license": true, "allowed-tools": true, "metadata": true,
	}
	for key := range values {
		if !allowed[key] {
			t.Errorf("%s has unsupported frontmatter key %q", filePath, key)
		}
	}

	name, nameOK := values["name"].(string)
	description, descriptionOK := values["description"].(string)
	if !nameOK || !skillNamePattern.MatchString(name) || len(name) > 64 {
		t.Errorf("%s has invalid skill name %q", filePath, name)
	}
	if !descriptionOK || strings.TrimSpace(description) == "" || len(description) > 1024 || strings.ContainsAny(description, "<>") {
		t.Errorf("%s has invalid description", filePath)
	}
	return skillFrontmatter{name: name, description: description}
}

func validateLocalLinks(t *testing.T, skillName, filePath, content string) {
	t.Helper()

	for _, match := range markdownLinkPattern.FindAllStringSubmatch(content, -1) {
		target := strings.TrimSpace(match[1])
		if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "/") || strings.Contains(target, "://") {
			continue
		}
		if before, _, ok := strings.Cut(target, "#"); ok {
			target = before
		}
		resolved := path.Clean(path.Join(path.Dir(filePath), target))
		if resolved != skillName && !strings.HasPrefix(resolved, skillName+"/") {
			t.Errorf("%s link %q escapes skill directory", filePath, match[1])
			continue
		}
		if _, err := fs.Stat(Assets, resolved); err != nil {
			t.Errorf("%s link %q resolves to missing %s: %v", filePath, match[1], resolved, err)
		}
	}
}
