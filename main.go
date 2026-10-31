package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/up2jj/wuko/cmd"
	"github.com/up2jj/wuko/elevation"
	"github.com/up2jj/wuko/localresource"
	"github.com/up2jj/wuko/useroutput"
)

func main() {
	if code, handled := elevation.HandleHelper(os.Args); handled {
		os.Exit(code)
	}
	args, err := localresource.WorkerArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wuko:", err)
		os.Exit(1)
	}
	os.Args = append(os.Args[:1], args...)
	if err := cmd.Execute(); err != nil {
		if !cmd.WriteError(os.Stderr, err) {
			fmt.Fprintln(os.Stderr, "wuko:", err)
		}
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, cmd.ErrForcedShutdown) {
		return 130
	}
	view := useroutput.Inspect(err)
	if view.Ordinary == nil && len(view.Failures) > 0 {
		if code := view.Failures[0].Code; code >= 1 && code <= 255 {
			return code
		}
	}
	return 1
}
