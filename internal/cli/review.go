package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/mpv"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review <sequence>",
		Short: "Play a sequence instantly in mpv (no render)",
		Args:  cobra.ExactArgs(1),
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
			name := strings.TrimSuffix(filepath.Base(args[0]), ".txt")
			return mpv.Review(p, name, items)
		},
	}
}
