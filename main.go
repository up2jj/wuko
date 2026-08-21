package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/up2jj/wuko/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wuko:", err)
		if errors.Is(err, cmd.ErrForcedShutdown) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
