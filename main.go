package main

import (
	"fmt"
	"os"

	"github.com/up2jj/wuko/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wuko:", err)
		os.Exit(1)
	}
}
