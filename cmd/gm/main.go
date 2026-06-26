package main

import (
	"fmt"
	"os"

	"github.com/greenmaskio/gm-tools/internal/adapter/cli"
	"github.com/greenmaskio/gm-tools/internal/client/compose"
	"github.com/greenmaskio/gm-tools/internal/cmd"
	"github.com/greenmaskio/gm-tools/internal/repository/config"
	"github.com/greenmaskio/gm-tools/internal/repository/manifest"
	"github.com/greenmaskio/gm-tools/internal/service"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "determine working directory:", err)
		os.Exit(1)
	}

	// DI root: construct repositories and clients, wire the service, and hand
	// it to the CLI adapter.
	cfgRepo := config.New(cwd)
	mfRepo := manifest.New(cwd)
	inspector := compose.NewClient()
	svc := service.New(cwd, cfgRepo, mfRepo, inspector)
	handlers := cli.New(svc)

	if err := cmd.NewRootCmd(handlers).Execute(); err != nil {
		os.Exit(1)
	}
}
