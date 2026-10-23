package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/up2jj/wuko/cmd"
	"github.com/up2jj/wuko/elevation"
	"github.com/up2jj/wuko/localresource"
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
	return 1
}
