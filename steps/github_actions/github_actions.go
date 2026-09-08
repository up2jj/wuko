// Package githubactions watches GitHub Actions workflow runs through the gh CLI.
package githubactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	operationWatch    = "watch"
	runJSONFields     = "attempt,conclusion,createdAt,databaseId,displayTitle,event,headBranch,headSha,name,number,startedAt,status,updatedAt,url,workflowDatabaseId,workflowName"
	watchCaptureLimit = 64 << 10
	logCaptureLimit   = 4 << 20
)

// Config selects a GitHub Actions run to watch and, optionally, one job or step log to return.
type Config struct {
	Operation   string `yaml:"operation"`
	Repository  string `yaml:"repository,omitempty"`
	Workflow    string `yaml:"workflow,omitempty"`
	RunID       string `yaml:"run_id,omitempty"`
	PullRequest string `yaml:"pull_request,omitempty"`
	HeadSHA     string `yaml:"head_sha,omitempty"`
	Job         string `yaml:"job,omitempty"`
	Step        string `yaml:"step,omitempty"`
	Interval    int    `yaml:"interval,omitempty"`
}

type Runner struct {
	config Config
}

type runRecord struct {
	Attempt            int         `json:"attempt"`
	Conclusion         string      `json:"conclusion"`
	CreatedAt          string      `json:"createdAt"`
	DatabaseID         int64       `json:"databaseId"`
	DisplayTitle       string      `json:"displayTitle"`
	Event              string      `json:"event"`
	HeadBranch         string      `json:"headBranch"`
	HeadSHA            string      `json:"headSha"`
	Jobs               []jobRecord `json:"jobs"`
	Name               string      `json:"name"`
	Number             int         `json:"number"`
	StartedAt          string      `json:"startedAt"`
	Status             string      `json:"status"`
	UpdatedAt          string      `json:"updatedAt"`
	URL                string      `json:"url"`
	WorkflowDatabaseID int64       `json:"workflowDatabaseId"`
	WorkflowName       string      `json:"workflowName"`
}

type jobRecord struct {
	CompletedAt string       `json:"completedAt"`
	Conclusion  string       `json:"conclusion"`
	DatabaseID  int64        `json:"databaseId"`
	Name        string       `json:"name"`
	StartedAt   string       `json:"startedAt"`
	Status      string       `json:"status"`
	Steps       []stepRecord `json:"steps"`
	URL         string       `json:"url"`
}

type stepRecord struct {
	CompletedAt string `json:"completedAt"`
	Conclusion  string `json:"conclusion"`
	Name        string `json:"name"`
	Number      int    `json:"number"`
	StartedAt   string `json:"startedAt"`
	Status      string `json:"status"`
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
	config.Operation = strings.TrimSpace(config.Operation)
	config.Repository = strings.TrimSpace(config.Repository)
	config.Workflow = strings.TrimSpace(config.Workflow)
	config.RunID = strings.TrimSpace(config.RunID)
	config.PullRequest = strings.TrimSpace(config.PullRequest)
	config.HeadSHA = strings.TrimSpace(config.HeadSHA)
	config.Job = strings.TrimSpace(config.Job)
	config.Step = strings.TrimSpace(config.Step)

	if config.Operation == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if config.Operation != operationWatch {
		if templated(config.Operation) {
			return nil, fmt.Errorf("operation must not be templated")
		}
		return nil, fmt.Errorf("operation must be %s", operationWatch)
	}
	if _, configured := raw["job"]; configured && config.Job == "" {
		return nil, fmt.Errorf("job must not be empty")
	}
	if _, configured := raw["step"]; configured && config.Step == "" {
		return nil, fmt.Errorf("step must not be empty")
	}
	if config.Step != "" && config.Job == "" {
		return nil, fmt.Errorf("job is required when step is set")
	}
	if _, configured := raw["interval"]; configured && config.Interval <= 0 {
		return nil, fmt.Errorf("interval must be a positive integer")
	}

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

	repository := resolveRepository(runner.config.Repository, request.Env)
	runID, selectedHeadSHA, err := runner.resolveRunID(ctx, request, repository)
	if err != nil {
		return step.Result{}, err
	}
	if err := runner.watchRun(ctx, request, repository, runID); err != nil {
		return step.Result{}, err
	}

	record, err := runner.viewRun(ctx, request, repository, runID, runner.config.Job != "")
	if err != nil {
		return step.Result{}, err
	}
	outputs := runOutputs(record)
	outputs["repository"] = repository
	if outputs["head_sha"] == "" {
		outputs["head_sha"] = selectedHeadSHA
	}
	if runner.config.Job == "" {
		return step.Result{Outputs: outputs}, nil
	}

	job, err := selectJob(record.Jobs, runner.config.Job)
	if err != nil {
		return step.Result{}, err
	}
	var selectedStep *stepRecord
	if runner.config.Step != "" {
		selectedStep, err = selectStep(job.Steps, runner.config.Step)
		if err != nil {
			return step.Result{}, err
		}
	}
	log, truncated, err := downloadJobLog(ctx, request, repository, job.DatabaseID)
	if err != nil {
		return step.Result{}, err
	}

	outputs["job_id"] = job.DatabaseID
	outputs["job"] = job.Name
	outputs["log_scope"] = "job"
	outputs["log_truncated"] = truncated
	if selectedStep != nil {
		log, err = scopeStepLog(log, *selectedStep)
		if err != nil {
			if truncated {
				return step.Result{}, fmt.Errorf("scoping log for GitHub Actions step %q: job log was truncated at %d bytes: %w", selectedStep.Name, int64(logCaptureLimit), err)
			}
			return step.Result{}, fmt.Errorf("scoping log for GitHub Actions step %q: %w", selectedStep.Name, err)
		}
		outputs["step"] = selectedStep.Name
		outputs["log_scope"] = "step"
	}
	outputs["log"] = log
	return step.Result{Outputs: outputs}, nil
}

func (runner *Runner) resolveRunID(ctx context.Context, request step.Request, repository string) (int64, string, error) {
	if runner.config.RunID != "" {
		runID, err := strconv.ParseInt(runner.config.RunID, 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("parsing run_id: %w", err)
		}
		return runID, runner.config.HeadSHA, nil
	}

	headSHA := runner.config.HeadSHA
	if runner.config.PullRequest != "" {
		pull, err := runner.viewPullRequest(ctx, request, repository, runner.config.PullRequest)
		if err != nil {
			return 0, "", err
		}
		headSHA = pull.HeadSHA
	}

	runs, err := runner.listRuns(ctx, request, repository, headSHA)
	if err != nil {
		return 0, "", err
	}
	if len(runs) == 0 {
		if headSHA == "" {
			return 0, "", fmt.Errorf("no GitHub Actions run found for workflow %q", runner.config.Workflow)
		}
		return 0, "", fmt.Errorf("no GitHub Actions run found for workflow %q and head SHA %q", runner.config.Workflow, headSHA)
	}
	if headSHA != "" && len(runs) > 1 {
		return 0, "", fmt.Errorf("found %d GitHub Actions runs for workflow %q and head SHA %q; run selection is ambiguous", len(runs), runner.config.Workflow, headSHA)
	}
	// Nothing narrows a bare workflow selector to the run the caller means, and GitHub takes
	// seconds to create a run that a preceding step triggered. Watching the newest completed
	// run would report someone else's conclusion as this step's, so refuse it loudly instead.
	if headSHA == "" && runs[0].Status == "completed" {
		return 0, "", fmt.Errorf("latest GitHub Actions run %d for workflow %q has already completed; select the intended run with run_id, pull_request, or head_sha, or retry once it has been created", runs[0].DatabaseID, runner.config.Workflow)
	}
	return runs[0].DatabaseID, headSHA, nil
}

func (runner *Runner) viewPullRequest(ctx context.Context, request step.Request, repository, number string) (pullRequest, error) {
	args := []string{"pr", "view", number, "--json", "headRefOid"}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, nil, args...)
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
	limit := "1"
	args := []string{"run", "list", "--workflow", runner.config.Workflow}
	if headSHA != "" {
		limit = "20"
		args = append(args, "--commit", headSHA)
	}
	args = append(args, "--limit", limit, "--json", runJSONFields)
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, nil, args...)
	if err != nil {
		return nil, commandError("listing GitHub Actions runs", result, err)
	}
	var runs []runRecord
	if err := json.Unmarshal([]byte(result.Stdout), &runs); err != nil {
		return nil, fmt.Errorf("decoding GitHub Actions runs: %w", err)
	}
	filtered := runs[:0]
	for _, run := range runs {
		if run.DatabaseID == 0 || headSHA != "" && run.HeadSHA != "" && !strings.EqualFold(run.HeadSHA, headSHA) {
			continue
		}
		filtered = append(filtered, run)
	}
	return filtered, nil
}

func (runner *Runner) watchRun(ctx context.Context, request step.Request, repository string, runID int64) error {
	args := []string{"run", "watch", strconv.FormatInt(runID, 10), "--compact"}
	if runner.config.Interval > 0 {
		args = append(args, "--interval", strconv.Itoa(runner.config.Interval))
	}
	args = withRepository(args, repository)
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: "gh", Args: args, Dir: request.RunDir,
		Env:    step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
		Stdout: request.Stdout, Stderr: request.Stderr,
		StdoutPolicy: process.OutputTee, StderrPolicy: process.OutputTee,
		CaptureLimit: watchCaptureLimit,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return commandError("watching GitHub Actions run", result, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err))
	}
	return nil
}

func (runner *Runner) viewRun(ctx context.Context, request step.Request, repository string, runID int64, includeJobs bool) (runRecord, error) {
	fields := runJSONFields
	if includeJobs {
		fields += ",jobs"
	}
	args := []string{"run", "view", strconv.FormatInt(runID, 10), "--json", fields}
	args = withRepository(args, repository)
	result, err := runCommand(ctx, request, nil, args...)
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

func selectJob(jobs []jobRecord, name string) (jobRecord, error) {
	matches := make([]jobRecord, 0, 1)
	for _, job := range jobs {
		if job.Name == name {
			matches = append(matches, job)
		}
	}
	if len(matches) == 0 {
		return jobRecord{}, fmt.Errorf("GitHub Actions job %q was not found", name)
	}
	if len(matches) > 1 {
		return jobRecord{}, fmt.Errorf("found %d GitHub Actions jobs named %q; job selection is ambiguous", len(matches), name)
	}
	if matches[0].DatabaseID == 0 {
		return jobRecord{}, fmt.Errorf("GitHub Actions job %q has no database ID", name)
	}
	return matches[0], nil
}

func selectStep(steps []stepRecord, name string) (*stepRecord, error) {
	matches := make([]stepRecord, 0, 1)
	for _, candidate := range steps {
		if candidate.Name == name {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("GitHub Actions step %q was not found", name)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("found %d GitHub Actions steps named %q; step selection is ambiguous", len(matches), name)
	}
	return &matches[0], nil
}

// downloadJobLog returns the job log and whether capture stopped at logCaptureLimit. Job logs
// are unbounded on GitHub's side and the whole log becomes a step output, so capture is capped
// rather than buffering an arbitrarily large body into the run state.
func downloadJobLog(ctx context.Context, request step.Request, repository string, jobID int64) (string, bool, error) {
	environment := map[string]string(nil)
	if repository != "" {
		environment = map[string]string{"GH_REPO": repository}
	}
	endpoint := fmt.Sprintf("repos/{owner}/{repo}/actions/jobs/%d/logs", jobID)
	result, err := runCommandWithLimit(ctx, request, environment, logCaptureLimit, "api", "--allow-escape-sequences", endpoint)
	if err != nil {
		return "", false, commandError("downloading GitHub Actions job log", result, err)
	}
	return result.Stdout, result.StdoutTruncated, nil
}

func scopeStepLog(log string, selected stepRecord) (string, error) {
	startedAt, err := time.Parse(time.RFC3339Nano, selected.StartedAt)
	if err != nil {
		return "", fmt.Errorf("parsing startedAt %q: %w", selected.StartedAt, err)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, selected.CompletedAt)
	if err != nil {
		return "", fmt.Errorf("parsing completedAt %q: %w", selected.CompletedAt, err)
	}
	if completedAt.Before(startedAt) {
		return "", fmt.Errorf("step interval must not complete before it starts")
	}
	completionPrecision, err := timestampPrecision(selected.CompletedAt)
	if err != nil {
		return "", fmt.Errorf("determining completedAt precision: %w", err)
	}
	// GitHub's structured step timestamps are commonly truncated to whole seconds, so a
	// step shares its first and last timestamp bucket with the adjacent steps. Both
	// buckets are kept: short steps emit most of their output inside them, and dropping
	// them loses far more than the neighboring lines they may add. The completion bucket
	// is covered by widening the end boundary by the completion timestamp's precision.
	end := completedAt.Add(completionPrecision)

	var scoped strings.Builder
	var current time.Time
	sawTimestamp := false
	selectedLine := false
	for _, line := range splitLinesAfter(log) {
		if timestamp, ok := parseLogTimestamp(line); ok {
			current = timestamp
			sawTimestamp = true
		} else if !sawTimestamp && strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")) != "" {
			return "", fmt.Errorf("job log starts without a parseable timestamp")
		}
		if sawTimestamp && !current.Before(startedAt) && current.Before(end) {
			scoped.WriteString(line)
			selectedLine = true
		}
	}
	if !sawTimestamp {
		return "", fmt.Errorf("job log contains no parseable timestamps")
	}
	if !selectedLine {
		return "", fmt.Errorf("job log contains no lines in the step interval")
	}
	return scoped.String(), nil
}

func splitLinesAfter(value string) []string {
	if value == "" {
		return nil
	}
	return strings.SplitAfter(value, "\n")
}

func parseLogTimestamp(line string) (time.Time, bool) {
	line = strings.TrimPrefix(line, "\ufeff")
	timestamp, _, ok := strings.Cut(line, " ")
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func timestampPrecision(value string) (time.Duration, error) {
	fraction := strings.IndexByte(value, '.')
	if fraction < 0 {
		return time.Second, nil
	}
	digits := 0
	for index := fraction + 1; index < len(value) && value[index] >= '0' && value[index] <= '9'; index++ {
		digits++
	}
	if digits == 0 || digits > 9 {
		return 0, fmt.Errorf("timestamp %q has unsupported fractional precision", value)
	}
	precision := time.Nanosecond
	for range 9 - digits {
		precision *= 10
	}
	return precision, nil
}

func runOutputs(record runRecord) map[string]any {
	workflow := record.WorkflowName
	if workflow == "" {
		workflow = record.Name
	}
	terminal := record.Status == "completed"
	return map[string]any{
		"run_id": record.DatabaseID, "run_number": record.Number,
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

func runCommand(ctx context.Context, request step.Request, extraEnvironment map[string]string, args ...string) (process.Result, error) {
	return runCommandWithLimit(ctx, request, extraEnvironment, 0, args...)
}

func runCommandWithLimit(ctx context.Context, request step.Request, extraEnvironment map[string]string, captureLimit int64, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	environment := step.ApplyAttemptEnvironment(maps.Clone(request.Env), request)
	maps.Copy(environment, extraEnvironment)
	result, err := executor.Run(ctx, process.Options{
		Command: "gh", Args: args, Dir: request.RunDir, Env: environment,
		StdoutPolicy: process.OutputCapture, StderrPolicy: process.OutputCapture,
		CaptureLimit: captureLimit,
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

func resolveRepository(configured string, environment map[string]string) string {
	for _, candidate := range []string{configured, environment["GITHUB_REPOSITORY"], environment["GH_REPO"]} {
		if repository := strings.TrimSpace(candidate); repository != "" {
			return repository
		}
	}
	return ""
}

func (runner *Runner) validateResolvedConfig() error {
	for _, value := range []string{
		runner.config.Repository, runner.config.Workflow,
		runner.config.RunID, runner.config.PullRequest, runner.config.HeadSHA,
		runner.config.Job, runner.config.Step,
	} {
		if templated(value) {
			return errors.New("github_actions configuration contains an unresolved template")
		}
	}
	return nil
}

func positiveInteger(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func templated(value string) bool { return strings.Contains(value, "{{") }

var _ step.ExecutorAware = (*Runner)(nil)
