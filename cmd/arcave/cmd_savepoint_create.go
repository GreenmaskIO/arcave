package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	savepointCreateName        string
	savepointCreateDescription string
	savepointCreateLabels      []string

	savepointCreateCmd = &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new environment savepoint",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runSavepointCreate,
	}
)

func runSavepointCreate(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	h := ngcli.NewSavepointCreateHandler(newAPI(), newPrinter())
	return h.Create(cmd.Context(), projectCfg, rootAddressing(), ngcli.SavepointCreateParams{
		Args:        args,
		Name:        savepointCreateName,
		Description: savepointCreateDescription,
		Labels:      savepointCreateLabels,
	})
}

func init() {
	flags := savepointCreateCmd.Flags()
	flags.StringVar(&savepointCreateName, "name", "", "Savepoint name")
	flags.StringVar(&savepointCreateDescription, "description", "", "Savepoint description")
	// StringArray, not StringSlice: a label value may hold a comma.
	flags.StringArrayVar(&savepointCreateLabels, "label", nil,
		"Label to mark the savepoint with, as key=value (repeatable)")

	// None is bound to viper: --name would collide with `init --name` on the shared "name" key.

	savepointCmd.AddCommand(savepointCreateCmd)
}
