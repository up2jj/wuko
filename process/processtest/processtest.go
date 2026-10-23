// Package processtest provides a fake process.Executor that records commands and answers them
// with scripted results instead of running anything.
package processtest

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/up2jj/wuko/process"
)

// Reply is the outcome an Executor returns for one command.
type Reply struct {
	Result process.Result
	Err    error
}

// Executor records every command it is asked to run. It answers them in order from Replies and,
// once those are used up, with Reply, or with an error when Strict is set. Like a real executor,
// it writes a reply's output to the command's Stdout and Stderr when their policies stream.
// It is safe for concurrent use.
type Executor struct {
	Replies []Reply
	Reply   Reply
	// Strict fails every command beyond Replies, so a test notices commands it did not expect.
	Strict bool

	mu    sync.Mutex
	calls []process.Options
}

// Run records options and returns the next reply.
func (executor *Executor) Run(_ context.Context, options process.Options) (process.Result, error) {
	options.Args = slices.Clone(options.Args)
	options.Env = maps.Clone(options.Env)
	executor.mu.Lock()
	executor.calls = append(executor.calls, options)
	index := len(executor.calls) - 1
	executor.mu.Unlock()

	reply := executor.Reply
	switch {
	case index < len(executor.Replies):
		reply = executor.Replies[index]
	case executor.Strict:
		return process.Result{}, fmt.Errorf("unexpected command %d: %s %s", index+1, options.Command, strings.Join(options.Args, " "))
	}
	if options.StdoutPolicy.Streams() && options.Stdout != nil {
		_, _ = io.WriteString(options.Stdout, reply.Result.Stdout)
	}
	if options.StderrPolicy.Streams() && options.Stderr != nil {
		_, _ = io.WriteString(options.Stderr, reply.Result.Stderr)
	}
	return reply.Result, reply.Err
}

// Calls returns the commands run so far, in order.
func (executor *Executor) Calls() []process.Options {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return slices.Clone(executor.calls)
}

// Last returns the most recent command, or zero Options when none has run.
func (executor *Executor) Last() process.Options {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.calls) == 0 {
		return process.Options{}
	}
	return executor.calls[len(executor.calls)-1]
}
