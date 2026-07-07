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
	"github.com/greenmaskio/gm-tools/internal/storages/directory"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "determine working directory:", err)
		os.Exit(1)
	}

	// DI root: construct repositories and clients, wire the service, and hand
	// it to the CLI adapter. The config repository is a pure writer (Save/Exists/
	// Path); reads go through appconfig.Resolve, resolved per command in RunE.
	cfgRepo := config.New(cwd)
	mfRepo := manifest.New(cwd)
	inspector := compose.NewClient()

	// st is the single Storager, rooted at the project directory; service and
	// repositories see it only as model.Storager, so the concrete backend is
	// swappable here without touching any other layer.
	st, err := directory.NewStorage(&directory.Config{Path: cwd})
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialise storage backend:", err)
		os.Exit(1)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "close storage backend:", cerr)
		}
	}()

	svc := service.New(cwd, cfgRepo, mfRepo, inspector, st)
	handlers := cli.New(svc, cli.NewJSONPrinter(os.Stdout))

	if err := cmd.NewRootCmd(handlers, cwd).Execute(); err != nil {
		os.Exit(1)
	}
}
