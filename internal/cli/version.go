package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/mpv"
)

// Version is the movielily build version.
const Version = "0.1.0"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the movielily version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("movielily %s  (watch: %s)\n", Version, mpv.WatchVersion)
			return nil
		},
	}
}
