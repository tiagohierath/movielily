package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"milklily/internal/mpv"
)

// Version is the milklily build version.
const Version = "0.1.0"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the milklily version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("milklily %s  (watch: %s)\n", Version, mpv.WatchVersion)
			return nil
		},
	}
}
