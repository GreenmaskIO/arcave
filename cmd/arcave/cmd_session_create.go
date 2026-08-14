package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var (
	sessionCreateFromSessionID string

	sessionCreateCmd = &cobra.Command{
		Use:   "create",
		Short: "Open a session in the project, from scratch or inherited",
		Args:  cobra.NoArgs,
		RunE:  runSessionCreate,
	}
)

func runSessionCreate(cmd *cobra.Command, _ []string) error {
	if err := requireUsableConfig(); err != nil {
		return err
	}

	h := ngcli.NewSessionCreateHandler(newAPI(), newPrinter())
	return h.Create(cmd.Context(), projectCfg, rootAddressing(), sessionCreateFromSessionID)
}

func init() {
	sessionCreateCmd.Flags().StringVar(&sessionCreateFromSessionID, "from-session-id", "",
		"Session to inherit from; omitted, the new session starts from scratch")

	sessionCmd.AddCommand(sessionCreateCmd)
}
