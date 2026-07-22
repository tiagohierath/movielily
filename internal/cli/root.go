// Package cli wires movielily's commands together with cobra.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "movielily",
		Short: "A minimal, notebook-style video editor (watch -> log -> select -> assemble -> export)",
		Long: "movielily is a small command-line companion to mpv and ffmpeg for making\n" +
			"short videos: watch footage, mark moments, keep selects, assemble sequences\n" +
			"from plain-text files, review them instantly, and export with ffmpeg.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newInitCmd(),
		newWatchCmd(),
		newMarkerCmd(),
		newSelectCmd(),
		newNoteCmd(),
		newSearchCmd(),
		newTagCmd(),
		newSeqCmd(),
		newEditCmd(),
		newReviewCmd(),
		newExportCmd(),
		newSilencesCmd(),
		newGradeCmd(),
		newChaptersCmd(),
		newFrameCmd(),
		newYoutubeCmd(),
		newSnapshotCmd(),
		newVersionCmd(),
	)
	return root
}

// Execute runs the CLI.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
