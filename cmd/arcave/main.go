package main

import (
	"context"
	"os"
)

// main executes the command tree. Config resolution and logger setup happen in
// rootCmd's PersistentPreRunE (cmd_root.go); each command builds the dependencies
// it needs via initAllDependencies (dependencies.go) and releases them on return.
func main() {
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		// cobra has already reported the error on stderr.
		os.Exit(1)
	}
}
