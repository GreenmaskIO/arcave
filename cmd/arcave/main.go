package main

import (
	"context"
	"os"
)

// main executes the command tree. A command that succeeded still exits through
// rootExitCode, which is how `recommend` reports a finding without an error.
func main() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		// cobra has already reported the error on stderr.
		os.Exit(1)
	}
	os.Exit(rootExitCode)
}
