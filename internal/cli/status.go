package cli

import "github.com/spf13/cobra"

func newStatusCommand(_ *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show this machine's config, system details and last sync",
		Args:  cobra.NoArgs,
		RunE:  notImplemented,
	}
}
