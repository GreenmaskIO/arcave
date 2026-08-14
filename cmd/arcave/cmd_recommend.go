package main

import (
	"github.com/spf13/cobra"

	ngcli "github.com/greenmaskio/arcave/internal/ng/cli"
)

var recommendCmd = &cobra.Command{
	Use:   "recommend",
	Short: "Report volumes the stack's services should mount but do not",
	Long: "Inspect the stack and report, as JSON, every persistent volume a service's image is " +
		"known to need and the stack does not declare — state a savepoint would silently fail to " +
		"capture.\n\n" +
		"It is read-only: it stops nothing and writes nothing.\n\n" +
		"Exits 1 when there is anything to recommend, 0 when there is not.",
	Args: cobra.NoArgs,
	RunE: runRecommend,
}

// runRecommend reports a finding through the exit status, not through an error.
func runRecommend(cmd *cobra.Command, _ []string) error {
	if err := requireConfig(); err != nil {
		return err
	}

	h := ngcli.NewRecommendHandler(newAPI(), newPrinter())
	found, err := h.Recommend(cmd.Context(), projectCfg, rootAddressing())
	if err != nil {
		return err
	}
	if found {
		rootExitCode = 1
	}
	return nil
}

func init() {
	rootCmd.AddCommand(recommendCmd)
}
