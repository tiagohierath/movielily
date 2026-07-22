package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/mpv"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newReviewCmd() *cobra.Command {
	var from int
	cmd := &cobra.Command{
		Use:   "review <sequence>",
		Short: "Watch the whole cut instantly in mpv (simulated export, no render)",
		Long: "review plays the sequence in mpv exactly as the export would look: clips\n" +
			"trimmed to their in/out, stills held for their duration, audio beds mixed\n" +
			"underneath, everything letterboxed into the project frame. Nothing is\n" +
			"rendered and nothing is written; it starts instantly at full resolution.",
		Args: cobra.ExactArgs(1),
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
			return mpv.ReviewFrom(p, name, items, itemIndexForScene(items, from))
		},
	}
	cmd.Flags().IntVar(&from, "from", 0, "start at this scene number (as shown by 'seq show')")
	return cmd
}

// itemIndexForScene maps a 1-based playable scene number (the numbering 'seq
// show' and the TUI display) to its index in items. 0 or out of range means
// "from the top".
func itemIndexForScene(items []model.SequenceItem, scene int) int {
	if scene <= 0 {
		return 0
	}
	n := 0
	for i, it := range items {
		if it.IsSection() || it.IsAudio() {
			continue
		}
		n++
		if n == scene {
			return i
		}
	}
	return 0
}
