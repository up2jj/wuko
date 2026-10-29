package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/up2jj/wuko/step"
)

const defaultBlockMarker = "# {mark} WUKO MANAGED BLOCK"

const (
	editNone     = "none"
	editAppended = "appended"
	editInserted = "inserted"
	editReplaced = "replaced"
	editCreated  = "created"
)

// editFields reports the fields an editing operation allows beyond operation and path,
// and the one that carries its text.
func editFields(operation string) ([]string, string) {
	common := []string{"paths", "create", "mode", "check"}
	switch operation {
	case operationAppend:
		return append(common, "content", "ensure_newline", "unless_contains", "unless_matches"), "content"
	case operationEnsureLine:
		return append(common, "line", "match", "present_if"), "line"
	default:
		return append(common, "content", "marker", "present_if"), "content"
	}
}

func (r *Runner) validateEdit(resolved bool) error {
	if r.present["path"] && r.present["paths"] {
		return fmt.Errorf("path and paths are mutually exclusive")
	}
	if r.present["paths"] {
		if len(r.config.Paths) == 0 {
			return fmt.Errorf("paths must contain at least one path")
		}
		for index, path := range r.config.Paths {
			if path == "" {
				return fmt.Errorf("paths[%d] must not be empty", index)
			}
		}
	}
	for field, value := range map[string]string{
		"unless_matches": r.config.UnlessMatches, "match": r.config.Match, "present_if": r.config.PresentIf,
	} {
		if value == "" || (!resolved && templated(value)) {
			continue
		}
		if _, err := regexp.Compile(value); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	switch r.config.Operation {
	case operationEnsureLine:
		if r.config.Line == "" {
			return fmt.Errorf("line must not be empty")
		}
		if strings.ContainsAny(r.config.Line, "\r\n") {
			return fmt.Errorf("line must not contain a line break")
		}
	case operationEnsureBlock:
		if !r.present["marker"] || (!resolved && templated(r.config.Marker)) {
			return nil
		}
		if !strings.Contains(r.config.Marker, "{mark}") {
			return fmt.Errorf("marker must contain {mark}")
		}
		if strings.ContainsAny(r.config.Marker, "\r\n") {
			return fmt.Errorf("marker must not contain a line break")
		}
	}
	return nil
}

// editCandidate is one file an editing operation searches, read before anything is decided.
type editCandidate struct {
	path    string
	exists  bool
	content string
}

// editPlan is the decision an editing operation reaches from its candidates: which file it
// writes, and either the text to append to it or its whole replacement content.
type editPlan struct {
	target  string
	matched string
	exists  bool
	action  string
	text    string
}

func (r *Runner) edit(ctx context.Context, runDir string) (step.Result, error) {
	paths := r.config.Paths
	if len(paths) == 0 {
		paths = []string{r.config.Path}
	}
	candidates := make([]editCandidate, 0, len(paths))
	for _, path := range paths {
		resolved, err := resolvePath(runDir, path)
		if err != nil {
			return step.Result{}, err
		}
		candidate, err := loadCandidate(ctx, resolved)
		if err != nil {
			return step.Result{}, err
		}
		candidates = append(candidates, candidate)
	}
	plan, err := r.decide(candidates)
	if err != nil {
		return step.Result{}, err
	}
	wouldChange := plan.action != editNone
	outputs := map[string]any{
		"path": plan.target, "matched_path": plan.matched, "exists": plan.exists, "action": plan.action,
		"would_change": wouldChange, "changed": false, "created": false, "bytes": int64(0),
	}
	if !wouldChange || r.config.Check {
		return step.Result{Outputs: outputs}, nil
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if plan.action == editReplaced {
		err = replaceFile(ctx, plan.target, plan.text)
	} else {
		err = r.appendFile(plan.target, plan.text, plan.exists)
	}
	if err != nil {
		return step.Result{}, err
	}
	outputs["changed"], outputs["created"], outputs["bytes"] = true, !plan.exists, int64(len(plan.text))
	return step.Result{Outputs: outputs}, nil
}

// loadCandidate reads one searched file. A missing file, or one under a missing or
// non-directory parent, is a candidate without content rather than an error.
func loadCandidate(ctx context.Context, path string) (editCandidate, error) {
	if err := ctx.Err(); err != nil {
		return editCandidate{}, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return editCandidate{path: path}, nil
	}
	if err != nil {
		return editCandidate{}, fmt.Errorf("inspecting file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return editCandidate{}, fmt.Errorf("path %s must be a regular file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return editCandidate{}, fmt.Errorf("reading file %s: %w", path, err)
	}
	return editCandidate{path: path, exists: true, content: string(content)}, nil
}

// decide reaches the operation's plan without touching the filesystem, so a check run
// and the write that follows it come to the same answer from the same files.
func (r *Runner) decide(candidates []editCandidate) (editPlan, error) {
	presentIf, err := compileOptional(r.config.PresentIf)
	if err != nil {
		return editPlan{}, err
	}
	satisfied := func(candidate editCandidate) editPlan {
		return editPlan{target: candidate.path, matched: candidate.path, exists: true, action: editNone}
	}
	var text string
	switch r.config.Operation {
	case operationAppend:
		unlessMatches, err := compileOptional(r.config.UnlessMatches)
		if err != nil {
			return editPlan{}, err
		}
		for _, candidate := range candidates {
			if !candidate.exists {
				continue
			}
			if (r.config.UnlessContains != "" && strings.Contains(candidate.content, r.config.UnlessContains)) ||
				anyLineMatches(unlessMatches, candidate.content) {
				return satisfied(candidate), nil
			}
		}
		text = r.config.Content
		if r.config.EnsureNewline && missingFinalNewline(candidates[0].content) {
			text = "\n" + text
		}
	case operationEnsureLine:
		match, err := compileOptional(r.config.Match)
		if err != nil {
			return editPlan{}, err
		}
		for _, candidate := range candidates {
			for _, line := range splitLines(candidate.content) {
				if line.text == r.config.Line || (presentIf != nil && presentIf.MatchString(line.text)) {
					return satisfied(candidate), nil
				}
			}
		}
		// Like lineinfile, the last matching line is the one replaced.
		for _, candidate := range candidates {
			if match == nil {
				break
			}
			lines := splitLines(candidate.content)
			for index := len(lines) - 1; index >= 0; index-- {
				if match.MatchString(lines[index].text) {
					content := candidate.content[:lines[index].start] + r.config.Line + candidate.content[lines[index].end:]
					return editPlan{target: candidate.path, matched: candidate.path, exists: true, action: editReplaced, text: content}, nil
				}
			}
		}
		text = r.config.Line + "\n"
		if missingFinalNewline(candidates[0].content) {
			text = "\n" + text
		}
	case operationEnsureBlock:
		marker := r.config.Marker
		if marker == "" {
			marker = defaultBlockMarker
		}
		begin, end := strings.ReplaceAll(marker, "{mark}", "BEGIN"), strings.ReplaceAll(marker, "{mark}", "END")
		body := r.config.Content
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		// A managed block is kept current wherever it is found, before present_if is
		// consulted, so a condition that matches the block's own old text cannot pin it.
		for _, candidate := range candidates {
			bodyStart, bodyEnd, found, err := findBlock(candidate, begin, end)
			if err != nil {
				return editPlan{}, err
			}
			if !found {
				continue
			}
			if candidate.content[bodyStart:bodyEnd] == body {
				return satisfied(candidate), nil
			}
			content := candidate.content[:bodyStart] + body + candidate.content[bodyEnd:]
			return editPlan{target: candidate.path, matched: candidate.path, exists: true, action: editReplaced, text: content}, nil
		}
		for _, candidate := range candidates {
			if anyLineMatches(presentIf, candidate.content) {
				return satisfied(candidate), nil
			}
		}
		text = begin + "\n" + body + end + "\n"
		if missingFinalNewline(candidates[0].content) {
			text = "\n" + text
		}
	}
	target := candidates[0]
	if !target.exists && !r.config.Create {
		return editPlan{}, fmt.Errorf("file %s does not exist; set create to true", target.path)
	}
	action := editCreated
	if target.exists {
		action = editInserted
		if r.config.Operation == operationAppend {
			action = editAppended
		}
	}
	return editPlan{target: target.path, exists: target.exists, action: action, text: text}, nil
}

// findBlock locates the body between a candidate's begin and end marker lines. A marker
// without its partner is an error: guessing where the block ends could discard user text.
func findBlock(candidate editCandidate, begin, end string) (int, int, bool, error) {
	beginLine, endLine := -1, -1
	lines := splitLines(candidate.content)
	for index, line := range lines {
		text := strings.TrimSpace(line.text)
		if beginLine < 0 && text == begin {
			beginLine = index
		} else if beginLine >= 0 && text == end {
			endLine = index
			break
		} else if beginLine < 0 && text == end {
			return 0, 0, false, fmt.Errorf("file %s has the end marker %q without its begin marker", candidate.path, end)
		}
	}
	switch {
	case beginLine < 0:
		return 0, 0, false, nil
	case endLine < 0:
		return 0, 0, false, fmt.Errorf("file %s has the begin marker %q without its end marker", candidate.path, begin)
	}
	return lines[beginLine].next, lines[endLine].start, true, nil
}

// textLine is one line of a file: text without its terminator, and the offsets of the
// text and of the next line.
type textLine struct {
	text             string
	start, end, next int
}

func splitLines(content string) []textLine {
	var lines []textLine
	for start := 0; start < len(content); {
		end, next := len(content), len(content)
		if index := strings.IndexByte(content[start:], '\n'); index >= 0 {
			end, next = start+index, start+index+1
		}
		if end > start && content[end-1] == '\r' {
			end--
		}
		lines = append(lines, textLine{text: content[start:end], start: start, end: end, next: next})
		start = next
	}
	return lines
}

func anyLineMatches(pattern *regexp.Regexp, content string) bool {
	if pattern == nil {
		return false
	}
	for _, line := range splitLines(content) {
		if pattern.MatchString(line.text) {
			return true
		}
	}
	return false
}

func compileOptional(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	return regexp.Compile(pattern)
}

func missingFinalNewline(content string) bool {
	return content != "" && !strings.HasSuffix(content, "\n")
}

// appendFile adds text through O_APPEND, which follows a symbolic link to its target and
// keeps the file's inode, mode, and owner. It is one append write, not an atomic one.
func (r *Runner) appendFile(path, text string, exists bool) (resultErr error) {
	flags, mode := os.O_WRONLY|os.O_APPEND, os.FileMode(0o644)
	if !exists {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if r.config.Mode != "" {
			parsed, err := parseMode(r.config.Mode)
			if err != nil {
				return err
			}
			mode = parsed
		}
	}
	output, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return fmt.Errorf("opening file %s: %w", path, err)
	}
	defer func() {
		if err := output.Close(); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("closing file %s: %w", path, err)
		}
	}()
	if !exists {
		if err := output.Chmod(mode); err != nil {
			return fmt.Errorf("setting file mode: %w", err)
		}
	}
	if _, err := output.WriteString(text); err != nil {
		return fmt.Errorf("appending to file %s: %w", path, err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("syncing file %s: %w", path, err)
	}
	if !exists {
		return syncDirectory(filepath.Dir(path))
	}
	return nil
}

// replaceFile rewrites the file a path resolves to, so a symbolic link survives and the
// file behind it changes. The replacement is atomic and keeps the mode; it is owned by
// the user running the workflow.
func replaceFile(ctx context.Context, path, content string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving file %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return fmt.Errorf("inspecting file %s: %w", real, err)
	}
	return atomicWrite(ctx, real, strings.NewReader(content), info.Mode().Perm(), true)
}
