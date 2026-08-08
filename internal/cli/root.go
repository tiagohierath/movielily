// Package cli wires milklily's commands together with cobra.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "milklily",
		Short: "A minimal short-film editor: browser boards, terminal timing, text EDLs",
		Long: "milklily is a small short-film system around plain-text EDLs: sort\n" +
			"storyboards in the browser, refine timing/audio in the terminal, review\n" +
			"instantly, and export with ffmpeg.",
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
		newBoardCmd(),
		newIntakeCmd(),
		newSketchCmd(),
		newSeqCmd(),
		newEditCmd(),
		newReviewCmd(),
		newExportCmd(),
		newEssayCmd(),
		newStoryboardCmd(),
		newSilencesCmd(),
		newGradeCmd(),
		newChaptersCmd(),
		newFrameCmd(),
		newYoutubeCmd(),
		newSnapshotCmd(),
		newDoctorCmd(),
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
