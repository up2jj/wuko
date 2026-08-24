// Package githubactions observes GitHub Actions workflow runs through the gh CLI.
package githubactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const runJSONFields = "attempt,conclusion,createdAt,databaseId,displayTitle,event,headBranch,headSha,name,number,startedAt,status,updatedAt,url,workflowDatabaseId,workflowName"

// Config selects one GitHub Actions run or describes how to discover it.
// Polling is intentionally owned by the workflow loop control; this step only observes once.
type Config struct {
	Repository  string `yaml:"repository,omitempty"`
	Workflow    string `yaml:"workflow,omitempty"`
	RunID       string `yaml:"run_id,omitempty"`
	PullRequest string `yaml:"pull_request,omitempty"`
	HeadSHA     string `yaml:"head_sha,omitempty"`
}

type Runner struct {
	config Config
}

type runRecord struct {
	Attempt            int    `json:"attempt"`
	Conclusion         string `json:"conclusion"`
	CreatedAt          string `json:"createdAt"`
	DatabaseID         int64  `json:"databaseId"`
	DisplayTitle       string `json:"displayTitle"`
	Event              string `json:"event"`
	HeadBranch         string `json:"headBranch"`
	HeadSHA            string `json:"headSha"`
	Name               string `json:"name"`
	Number             int    `json:"number"`
	StartedAt          string `json:"startedAt"`
	Status             string `json:"status"`
	UpdatedAt          string `json:"updatedAt"`
	URL                string `json:"url"`
	WorkflowDatabaseID int64  `json:"workflowDatabaseId"`
	WorkflowName       string `json:"workflowName"`
}

type pullRequest struct {
	HeadSHA string `json:"headRefOid"`
}

func Register(registry *step.Registry) error {
	return registry.Register("github_actions", New)
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	config.Repository = strings.TrimSpace(config.Repository)
	config.Workflow = strings.TrimSpace(config.Workflow)
	config.RunID = strings.TrimSpace(config.RunID)
	config.PullRequest = strings.TrimSpace(config.PullRequest)
	config.HeadSHA = strings.TrimSpace(config.HeadSHA)

	selectors := 0
	if config.RunID != "" {
		selectors++
	}
	if config.PullRequest != "" {
		selectors++
	}
	if config.HeadSHA != "" {
		selectors++
	}
	if selectors > 1 {
		return nil, fmt.Errorf("run_id, pull_request, and head_sha are mutually exclusive")
	}
	if config.RunID == "" && config.Workflow == "" {
		return nil, fmt.Errorf("workflow is required when run_id is not set")
	}
	if config.PullRequest != "" && !templated(config.PullRequest) && !positiveInteger(config.PullRequest) {
		return nil, fmt.Errorf("pull_request must be a positive integer")
	}
	if config.RunID != "" && !templated(config.RunID) && !positiveInteger(config.RunID) {
		return nil, fmt.Errorf("run_id must be a positive integer")
	}
	if config.HeadSHA != "" && !templated(config.HeadSHA) && len(config.HeadSHA) != 40 && len(config.HeadSHA) != 64 {
		return nil, fmt.Errorf("head_sha must be a 40- or 64-character commit SHA")
	}
	return &Runner{config: config}, nil
}

func (*Runner) ExecutorAware() {}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := runner.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}

	repository := runner.config.Repository
	if repository == "" {
		repository = strings.TrimSpace(request.Env["GITHUB_REPOSITORY"])
	}
	if runner.config.RunID != "" {
		runID, err := strconv.ParseInt(runner.config.RunID, 10, 64)
		if err != nil {
			return step.Result{}, fmt.Errorf("parsing run_id: %w", err)
		}
		record, err := runner.viewRun(ctx, request, repository, runID)
		if err != nil {
			return step.Result{}, err
		}
		outputs := runOutputs(record, true)
		outputs["repository"] = repository
		return step.Result{Outputs: outputs}, nil
	}

	headSHA := runner.config.HeadSHA
	if runner.config.PullRequest != "" {
		pull, err := runner.viewPullRequest(ctx, request, repository, runner.config.PullRequest)
		if err != nil {
			return step.Result{}, err
		}
		headSHA = pull.HeadSHA
	}
	if headSHA == "" {
		return step.Result{}, fmt.Errorf("head_sha or pull_request is required when run_id is not set")
	}

	runs, err := runner.listRuns(ctx, request, repository, headSHA)
	if err != nil {
		return step.Result{}, err
	}
	if len(runs) == 0 {
		outputs := runOutputs(runRecord{
			Status:       "not_found",
			HeadSHA:      headSHA,
			WorkflowName: runner.config.Workflow,
		}, false)
		outputs["repository"] = repository
		return step.Result{Outputs: outputs}, nil
	}
	if len(runs) > 1 {
		return step.Result{}, fmt.Errorf("found %d GitHub Actions runs for workflow %q and head SHA %q; run selection is ambiguous", len(runs), runner.config.Workflow, headSHA)
	}

	record, err := runner.viewRun(ctx, request, repository, runs[0].DatabaseID)
	if err != nil {
		return step.Result{}, err
	}
	outputs := runOutputs(record, true)
	outputs["repository"] = repository
	if outputs["head_sha"] == "" {
		outputs["head_sha"] = headSHA
	}
	return step.Result{Outputs: outputs}, nil
}

func (runner *Runner) viewPullRequest(ctx context.Context, request step.Request, repository, number string) (pullRequest, error) {
	args := []string{"pr", "view", number, "--json", "headRefOid"}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, args...)
	if err != nil {
		return pullRequest{}, commandError("viewing GitHub pull request", result, err)
	}
	var pull pullRequest
	if err := json.Unmarshal([]byte(result.Stdout), &pull); err != nil {
		return pullRequest{}, fmt.Errorf("decoding GitHub pull request: %w", err)
	}
	if strings.TrimSpace(pull.HeadSHA) == "" {
		return pullRequest{}, fmt.Errorf("decoding GitHub pull request: headRefOid is missing")
	}
	return pull, nil
}

func (runner *Runner) listRuns(ctx context.Context, request step.Request, repository, headSHA string) ([]runRecord, error) {
	args := []string{"run", "list", "--workflow", runner.config.Workflow, "--commit", headSHA, "--limit", "20", "--json", runJSONFields}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, args...)
	if err != nil {
		return nil, commandError("listing GitHub Actions runs", result, err)
	}
	var runs []runRecord
	if err := json.Unmarshal([]byte(result.Stdout), &runs); err != nil {
		return nil, fmt.Errorf("decoding GitHub Actions runs: %w", err)
	}
	filtered := runs[:0]
	for _, run := range runs {
		if run.DatabaseID == 0 || run.HeadSHA != "" && !strings.EqualFold(run.HeadSHA, headSHA) {
			continue
		}
		filtered = append(filtered, run)
	}
	return filtered, nil
}

func (runner *Runner) viewRun(ctx context.Context, request step.Request, repository string, runID int64) (runRecord, error) {
	args := []string{"run", "view", strconv.FormatInt(runID, 10), "--json", runJSONFields}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, args...)
	if err != nil {
		return runRecord{}, commandError("viewing GitHub Actions run", result, err)
	}
	var record runRecord
	if err := json.Unmarshal([]byte(result.Stdout), &record); err != nil {
		return runRecord{}, fmt.Errorf("decoding GitHub Actions run: %w", err)
	}
	if record.DatabaseID == 0 {
		record.DatabaseID = runID
	}
	return record, nil
}

func runOutputs(record runRecord, found bool) map[string]any {
	workflow := record.WorkflowName
	if workflow == "" {
		workflow = record.Name
	}
	terminal := record.Status == "completed"
	return map[string]any{
		"found": found, "run_id": record.DatabaseID, "run_number": record.Number,
		"workflow": workflow, "workflow_id": record.WorkflowDatabaseID,
		"status": record.Status, "conclusion": record.Conclusion,
		"terminal": terminal, "success": terminal && record.Conclusion == "success",
		"display_title": record.DisplayTitle, "event": record.Event,
		"head_sha": record.HeadSHA, "head_branch": record.HeadBranch,
		"url": record.URL, "attempt": record.Attempt,
		"created_at": record.CreatedAt, "started_at": record.StartedAt,
		"updated_at": record.UpdatedAt,
	}
}

func runCommand(ctx context.Context, request step.Request, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: "gh", Args: args, Dir: request.RunDir,
		Env: step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return result, nil
}

func commandError(action string, result process.Result, err error) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s: %w", action, detail, err)
}

func withRepository(args []string, repository string) []string {
	if repository == "" {
		return args
	}
	return append(args, "--repo", repository)
}

func (runner *Runner) validateResolvedConfig() error {
	if strings.Contains(runner.config.Repository, "{{") || strings.Contains(runner.config.Workflow, "{{") || strings.Contains(runner.config.RunID, "{{") || strings.Contains(runner.config.PullRequest, "{{") || strings.Contains(runner.config.HeadSHA, "{{") {
		return errors.New("github_actions configuration contains an unresolved template")
	}
	return nil
}

func positiveInteger(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func templated(value string) bool { return strings.Contains(value, "{{") }

var _ step.ExecutorAware = (*Runner)(nil)
