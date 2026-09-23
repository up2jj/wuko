package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/up2jj/wuko/cmd"
	"github.com/up2jj/wuko/elevation"
)

func main() {
	if code, handled := elevation.HandleHelper(os.Args); handled {
		os.Exit(code)
	}
	if err := cmd.Execute(); err != nil {
		if !cmd.WriteError(os.Stderr, err) {
			fmt.Fprintln(os.Stderr, "wuko:", err)
		}
		if errors.Is(err, cmd.ErrForcedShutdown) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
