package main

import (
	"os"

	"github.com/greenmaskio/gm-tools/internal/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
