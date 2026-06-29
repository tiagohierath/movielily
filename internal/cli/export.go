package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/ffmpeg"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <sequence> <output.mp4>",
		Short: "Render a sequence to a single video with ffmpeg",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			items, err := store.LoadSequence(p.Sequence(args[0]))
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return fmt.Errorf("sequence %q is empty or missing", args[0])
			}
			fmt.Printf("exporting %q (%d item(s)) -> %s\n", args[0], len(items), args[1])
			if err := ffmpeg.Export(p, items, args[1]); err != nil {
				return err
			}
			fmt.Printf("done: %s\n", args[1])
			return nil
		},
	}
}
