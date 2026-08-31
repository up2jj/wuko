package process

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	processpkg "github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type runningProcess struct {
	cancel   context.CancelFunc
	started  <-chan struct{}
	exited   chan processExit
	finished *atomic.Bool
}

type processExit struct {
	result processpkg.Result
	err    error
}

func (runner *Runner) runLifecycle(ctx, startupCtx context.Context, request step.Request, label string, ready chan<- error, committed, abandoned <-chan struct{}) error {
	restarts := 0
	announced := false
	for {
		matcher := newLogMatcher(runner.logPattern)
		process := runner.launch(ctx, request, label, matcher)
		if !announced {
			if err := runner.waitReady(startupCtx, request, process, matcher); err != nil {
				return errors.Join(err, runner.abortLaunch(ctx, request, label, process))
			}
			ready <- nil
			select {
			case <-committed:
			case <-abandoned:
				return runner.abortLaunch(ctx, request, label, process)
			case <-ctx.Done():
				return errors.Join(ctx.Err(), runner.abortLaunch(ctx, request, label, process))
			}
			announced = true
		}

		var exit processExit
		var livenessErr error
		var stopped bool
		if runner.config.Detached {
			select {
			case exit = <-process.exited:
			case <-ctx.Done():
				stopped = true
			}
			if !stopped && runner.exitError(exit) == nil {
				livenessErr, stopped = runner.waitDetached(ctx, request)
			}
		} else {
			exit, livenessErr, stopped = runner.waitRunning(ctx, request, process)
		}
		if stopped {
			return runner.shutdown(ctx, request, label, process)
		}
		if livenessErr != nil {
			// A liveness failure stops the service the same way the lifecycle does, so a
			// configured shutdown command runs before any restart replaces the instance.
			if shutdownErr := runner.shutdown(ctx, request, label, process); shutdownErr != nil {
				livenessErr = errors.Join(livenessErr, shutdownErr)
			}
		}
		failure := livenessErr
		if failure == nil {
			failure = runner.exitError(exit)
		}
		shouldRestart := runner.config.Restart.Policy == "always" || (runner.config.Restart.Policy == "on_failure" && failure != nil)
		if shouldRestart && (runner.config.Restart.MaxRestarts == 0 || restarts < runner.config.Restart.MaxRestarts) {
			restarts++
			if err := waitContext(ctx, duration(runner.config.Restart.Backoff, time.Second)); err != nil {
				return ctx.Err()
			}
			continue
		}
		return failure
	}
}

func (runner *Runner) launch(parent context.Context, request step.Request, label string, matcher *logMatcher) runningProcess {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
	started := make(chan struct{})
	exited := make(chan processExit, 1)
	var once sync.Once
	finished := &atomic.Bool{}
	options, flush, optionsErr := runner.processOptions(request, label, func() { once.Do(func() { close(started) }) }, matcher)
	executor := request.Executor
	if executor == nil {
		executor = processpkg.LocalExecutor{}
	}
	go func() {
		if optionsErr != nil {
			finished.Store(true)
			exited <- processExit{err: optionsErr}
			return
		}
		result, err := executor.Run(runCtx, options)
		if flushErr := flush(); flushErr != nil {
			err = errors.Join(err, fmt.Errorf("flushing process output: %w", flushErr))
		}
		finished.Store(true)
		exited <- processExit{result: result, err: err}
	}()
	return runningProcess{cancel: cancel, started: started, exited: exited, finished: finished}
}

// abortLaunch stops a service that never committed to its scope. A detached launcher exits
// once its child is running, so only a configured shutdown command can reach that child.
func (runner *Runner) abortLaunch(ctx context.Context, request step.Request, label string, process runningProcess) error {
	if runner.config.Detached && runner.config.Shutdown.Command != nil {
		return runner.shutdown(ctx, request, label, process)
	}
	stopProcess(process)
	return nil
}

func stopProcess(process runningProcess) {
	process.cancel()
	if !process.finished.Load() {
		<-process.exited
	}
}

func (runner *Runner) waitReady(ctx context.Context, request step.Request, process runningProcess, matcher *logMatcher) error {
	// Started is closed by the executor before it can publish an exit. Prefer that established
	// ordering when both notifications are already selectable, so scheduler timing cannot turn a
	// successful spawn into a startup failure.
	select {
	case <-process.started:
		goto started
	default:
	}
	select {
	case <-process.started:
	case exit := <-process.exited:
		select {
		case <-process.started:
			process.exited <- exit
			goto started
		default:
		}
		return earlyExitError("process exited before it became ready", runner.exitError(exit))
	case <-ctx.Done():
		return ctx.Err()
	}
started:
	if runner.config.Readiness == nil {
		return nil
	}
	if log := runner.config.Readiness.Log; log != nil {
		timer := time.NewTimer(duration(log.Timeout, 30*time.Second))
		defer timer.Stop()
		if runner.config.Detached {
			select {
			case <-matcher.ready:
				return nil
			default:
			}
			select {
			case <-matcher.ready:
				return nil
			case <-timer.C:
				return fmt.Errorf("readiness log did not match %q within %s", log.Pattern, duration(log.Timeout, 30*time.Second))
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-matcher.ready:
			return nil
		default:
		}
		select {
		case <-matcher.ready:
			return nil
		case exit := <-process.exited:
			select {
			case <-matcher.ready:
				process.exited <- exit
				return nil
			default:
			}
			return earlyExitError("process exited before readiness log matched", runner.exitError(exit))
		case <-timer.C:
			return fmt.Errorf("readiness log did not match %q within %s", log.Pattern, duration(log.Timeout, 30*time.Second))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var timing ProbeTiming
	var probe func(context.Context) error
	if configured := runner.config.Readiness.Exec; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.execProbe(request, *configured)
	}
	if configured := runner.config.Readiness.HTTP; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.httpProbe(*configured)
	}
	exited := process.exited
	if runner.config.Detached {
		exited = nil
	}
	return runProbeUntil(ctx, timing, probe, exited)
}

func (runner *Runner) waitDetached(ctx context.Context, request step.Request) (error, bool) {
	if runner.config.Liveness == nil {
		<-ctx.Done()
		return nil, true
	}
	var timing ProbeTiming
	var probe func(context.Context) error
	if configured := runner.config.Liveness.Exec; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.execProbe(request, *configured)
	}
	if configured := runner.config.Liveness.HTTP; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.httpProbe(*configured)
	}
	if err := waitContext(ctx, duration(timing.InitialDelay, 0)); err != nil {
		return nil, true
	}
	failures := 0
	for {
		probeCtx, cancel := context.WithTimeout(ctx, duration(timing.Timeout, time.Second))
		err := probe(probeCtx)
		cancel()
		if err != nil {
			failures++
		} else {
			failures = 0
		}
		if failures >= threshold(timing.FailureThreshold, 3) {
			return fmt.Errorf("liveness probe failed %d times: %w", failures, err), false
		}
		if err := waitContext(ctx, duration(timing.Period, 10*time.Second)); err != nil {
			return nil, true
		}
	}
}

func (runner *Runner) waitRunning(ctx context.Context, request step.Request, process runningProcess) (processExit, error, bool) {
	if runner.config.Liveness == nil {
		select {
		case exit := <-process.exited:
			return exit, nil, false
		case <-ctx.Done():
			return processExit{}, nil, true
		}
	}
	var timing ProbeTiming
	var probe func(context.Context) error
	if configured := runner.config.Liveness.Exec; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.execProbe(request, *configured)
	}
	if configured := runner.config.Liveness.HTTP; configured != nil {
		timing = configured.ProbeTiming
		probe = runner.httpProbe(*configured)
	}
	if err := waitContext(ctx, duration(timing.InitialDelay, 0)); err != nil {
		return processExit{}, nil, true
	}
	failures := 0
	for {
		select {
		case exit := <-process.exited:
			return exit, nil, false
		case <-ctx.Done():
			return processExit{}, nil, true
		default:
		}
		probeCtx, cancel := context.WithTimeout(ctx, duration(timing.Timeout, time.Second))
		err := probe(probeCtx)
		cancel()
		if err != nil {
			failures++
		} else {
			failures = 0
		}
		if failures >= threshold(timing.FailureThreshold, 3) {
			// Leave the process running: runLifecycle stops it through shutdown so that a
			// configured shutdown command reaches services signals cannot address.
			return processExit{}, fmt.Errorf("liveness probe failed %d times: %w", failures, err), false
		}
		if err := waitContext(ctx, duration(timing.Period, 10*time.Second)); err != nil {
			return processExit{}, nil, true
		}
	}
}

func (runner *Runner) shutdown(scopeCtx context.Context, request step.Request, label string, process runningProcess) error {
	if runner.config.Shutdown.Command != nil {
		timeout := duration(runner.config.Shutdown.Timeout, 10*time.Second)
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(scopeCtx), timeout)
		command, args := buildCommand(runner.config.Shutdown.Command.Command, runner.config.Shutdown.Command.Script, runner.config.Shutdown.Command.Shell, runner.config.Shutdown.Command.Args)
		executor := request.Executor
		if executor == nil {
			executor = processpkg.LocalExecutor{}
		}
		dir, environment := runner.executionContext(request)
		stdout := prefixedWriter(request.Stdout, label+"/shutdown", nil).(*linePrefixWriter)
		stderr := prefixedWriter(request.Stderr, label+"/shutdown", nil).(*linePrefixWriter)
		_, commandErr := executor.Run(shutdownCtx, processpkg.Options{Command: command, Args: args, Dir: dir, Env: environment,
			Stdout: stdout, Stderr: stderr,
			StdoutPolicy: processpkg.OutputInherit, StderrPolicy: processpkg.OutputInherit})
		commandErr = errors.Join(commandErr, stdout.Flush(), stderr.Flush())
		cancel()
		if commandErr != nil {
			stopProcess(process)
			return fmt.Errorf("shutdown command: %w", commandErr)
		}
		if runner.config.Detached {
			// The detached child is the shutdown command's responsibility; the launcher
			// itself has usually exited already, and must not outlive the scope if it has not.
			stopProcess(process)
			return nil
		}
		select {
		case <-process.exited:
			return nil
		case <-time.After(timeout):
		}
	}
	stopProcess(process)
	return nil
}

func (runner *Runner) exitError(exit processExit) error {
	if slices.Contains(runner.config.AllowedExitCodes, exit.result.ExitCode) {
		var processExitError *processpkg.ExitError
		if exit.err == nil || errors.As(exit.err, &processExitError) {
			return nil
		}
	}
	if exit.err != nil {
		return exit.err
	}
	return &processpkg.ExitError{Command: runner.config.Command, Code: exit.result.ExitCode}
}

func (runner *Runner) execProbe(request step.Request, configured ExecProbe) func(context.Context) error {
	return func(ctx context.Context) error {
		executor := request.Executor
		if executor == nil {
			executor = processpkg.LocalExecutor{}
		}
		dir, environment := runner.executionContext(request)
		result, err := executor.Run(ctx, processpkg.Options{Command: configured.Command, Args: configured.Args, Dir: dir,
			Env: environment, StdoutPolicy: processpkg.OutputDiscard, StderrPolicy: processpkg.OutputDiscard})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("probe exited with status %d", result.ExitCode)
		}
		return nil
	}
}

func (runner *Runner) httpProbe(configured HTTPProbe) func(context.Context) error {
	return func(ctx context.Context) error {
		method := configured.Method
		if method == "" {
			method = http.MethodGet
		}
		req, err := http.NewRequestWithContext(ctx, method, configured.URL, nil)
		if err != nil {
			return err
		}
		for name, value := range configured.Headers {
			req.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		response.Body.Close()
		expected := configured.ExpectedStatus
		if len(expected) == 0 {
			expected = []int{http.StatusOK}
		}
		if !slices.Contains(expected, response.StatusCode) {
			return fmt.Errorf("HTTP probe returned status %d", response.StatusCode)
		}
		return nil
	}
}

func runProbeUntil(ctx context.Context, timing ProbeTiming, probe func(context.Context) error, exited <-chan processExit) error {
	if err := waitContext(ctx, duration(timing.InitialDelay, 0)); err != nil {
		return err
	}
	successes, failures := 0, 0
	for {
		probeCtx, cancel := context.WithTimeout(ctx, duration(timing.Timeout, time.Second))
		err := probe(probeCtx)
		cancel()
		if err == nil {
			successes++
			failures = 0
		} else {
			successes = 0
			failures++
		}
		if successes >= threshold(timing.SuccessThreshold, 1) {
			return nil
		}
		if failures >= threshold(timing.FailureThreshold, 3) {
			return fmt.Errorf("readiness probe failed %d times: %w", failures, err)
		}
		timer := time.NewTimer(duration(timing.Period, 10*time.Second))
		select {
		case exit := <-exited:
			timer.Stop()
			return earlyExitError("process exited before it became ready", exit.err)
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func earlyExitError(message string, err error) error {
	if err == nil {
		return fmt.Errorf("%s with an allowed exit code", message)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func waitContext(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func duration(value *workflow.Duration, fallback time.Duration) time.Duration {
	if value == nil {
		return fallback
	}
	return value.Value()
}
func threshold(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func validateProbes(config Config) error {
	var timings []ProbeTiming
	if config.Readiness != nil {
		if config.Readiness.Exec != nil {
			timings = append(timings, config.Readiness.Exec.ProbeTiming)
		}
		if config.Readiness.HTTP != nil {
			timings = append(timings, config.Readiness.HTTP.ProbeTiming)
		}
	}
	if config.Liveness != nil {
		if config.Liveness.Exec != nil {
			timings = append(timings, config.Liveness.Exec.ProbeTiming)
		}
		if config.Liveness.HTTP != nil {
			timings = append(timings, config.Liveness.HTTP.ProbeTiming)
		}
	}
	for _, timing := range timings {
		if timing.SuccessThreshold < 0 || timing.FailureThreshold < 0 {
			return fmt.Errorf("probe thresholds cannot be negative")
		}
		for _, configured := range []*workflow.Duration{timing.InitialDelay, timing.Period, timing.Timeout} {
			if configured != nil && configured.Value() < 0 {
				return fmt.Errorf("probe durations cannot be negative")
			}
		}
	}
	if config.Readiness != nil && config.Readiness.Log != nil && config.Readiness.Log.Timeout != nil && config.Readiness.Log.Timeout.Value() <= 0 {
		return fmt.Errorf("readiness log timeout must be positive")
	}
	if config.Restart.Backoff != nil && config.Restart.Backoff.Value() < 0 {
		return fmt.Errorf("restart backoff cannot be negative")
	}
	if config.Shutdown.Timeout != nil && config.Shutdown.Timeout.Value() <= 0 {
		return fmt.Errorf("shutdown timeout must be positive")
	}
	if config.Readiness != nil && config.Readiness.Exec != nil && strings.TrimSpace(config.Readiness.Exec.Command) == "" {
		return fmt.Errorf("readiness exec command is required")
	}
	if config.Liveness != nil && config.Liveness.Exec != nil && strings.TrimSpace(config.Liveness.Exec.Command) == "" {
		return fmt.Errorf("liveness exec command is required")
	}
	if config.Readiness != nil && config.Readiness.HTTP != nil && strings.TrimSpace(config.Readiness.HTTP.URL) == "" {
		return fmt.Errorf("readiness http url is required")
	}
	if config.Liveness != nil && config.Liveness.HTTP != nil && strings.TrimSpace(config.Liveness.HTTP.URL) == "" {
		return fmt.Errorf("liveness http url is required")
	}
	return nil
}

func parseSignal(value string) (syscall.Signal, error) {
	signals := map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "TERM": syscall.SIGTERM, "SIGINT": syscall.SIGINT, "INT": syscall.SIGINT, "SIGHUP": syscall.SIGHUP, "HUP": syscall.SIGHUP, "SIGQUIT": syscall.SIGQUIT, "QUIT": syscall.SIGQUIT}
	signal, ok := signals[strings.ToUpper(value)]
	if !ok {
		return 0, fmt.Errorf("shutdown signal must be SIGTERM, SIGINT, SIGHUP, or SIGQUIT")
	}
	return signal, nil
}
