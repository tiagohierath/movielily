package cli

import (
	"github.com/spf13/cobra"

	"movielily/internal/mpv"
	"movielily/internal/project"
)

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch <clip>",
		Short: "Open a clip in mpv and log markers/selects from key presses",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			return mpv.Watch(p, args[0])
		},
	}
}
