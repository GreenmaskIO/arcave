package main

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/cli"
)

var (
	savepointCreateName        string
	savepointCreateDescription string
	savepointCreateWaitTimeout int

	savepointCreateCmd = &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new environment savepoint",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runSavepointCreate,
	}
)

func runSavepointCreate(cmd *cobra.Command, args []string) error {
	name, err := savepointCreateResolveName(args)
	if err != nil {
		return err
	}
	if err := requireConfig(); err != nil {
		return err
	}
	return withDeps(cmd, func(ctx context.Context, d *dependencies) error {
		h := cli.NewSavepointCreateHandler(d.service, d.printer)
		return h.SavepointCreate(ctx, projectCfg, name, savepointCreateDescription, savepointCreateWaitTimeout)
	})
}

// savepointCreateResolveName reconciles the optional positional name with
// --name. Supplying both is only an error when they disagree.
func savepointCreateResolveName(args []string) (string, error) {
	if len(args) == 0 {
		return savepointCreateName, nil
	}
	if savepointCreateName != "" && savepointCreateName != args[0] {
		return "", errors.New("savepoint name given both as argument and --name")
	}
	return args[0], nil
}

func init() {
	flags := savepointCreateCmd.Flags()
	flags.StringVar(&savepointCreateName, "name", "", "Savepoint name")
	flags.StringVar(&savepointCreateDescription, "description", "", "Savepoint description")
	flags.IntVar(&savepointCreateWaitTimeout, "wait-timeout", defaultUpWaitTimeout,
		"Seconds to wait for services to become healthy after the snapshot")

	// None of these is bound to viper: --name would collide with `arcave init --name`
	// on the shared "name" config key, conflating the project name with this
	// savepoint's name. All are per-run values, not project config.

	savepointCmd.AddCommand(savepointCreateCmd)
}
