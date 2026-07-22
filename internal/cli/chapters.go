package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

// newChaptersCmd prints YouTube chapters from the sequence's sections: each
// section header becomes a chapter at the timeline second it starts. Paste
// the output straight into the video description.
func newChaptersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chapters <sequence>",
		Short: "YouTube chapters from the sequence's sections (paste into the description)",
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
			if items, err = store.Expand(p.SequencesDir(), items); err != nil {
				return err
			}
			type ch struct {
				at    float64
				title string
			}
			var chs []ch
			offset, n := 0.0, 0
			for _, it := range items {
				if it.IsSection() {
					n++
					title := it.Note
					if title == "" {
						title = fmt.Sprintf("Parte %d", n)
					}
					chs = append(chs, ch{offset, title})
				}
				offset += it.Duration()
			}
			if len(chs) == 0 {
				return fmt.Errorf("sequence %q has no sections (add them with o in the TUI)", args[0])
			}
			// YouTube requires the first chapter to start at 0:00.
			if chs[0].at > 0 {
				chs = append([]ch{{0, "Início"}}, chs...)
			}
			for _, c := range chs {
				fmt.Printf("%s %s\n", clockChapters(c.at), c.title)
			}
			if len(chs) < 3 {
				fmt.Println("\n(YouTube needs at least 3 chapters, each 10s or longer, to show them)")
			}
			return nil
		},
	}
}

// clockChapters renders seconds the way YouTube expects: M:SS or H:MM:SS.
func clockChapters(t float64) string {
	s := int(t)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// newFrameCmd grabs one full-resolution frame as a PNG: the thumbnail
// workflow. Footage is only read; the PNG must land outside footage/.
func newFrameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "frame <clip> <time> <out.png>",
		Short: "Save a full-resolution frame as PNG (for thumbnails)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			abs, err := p.ResolveFootage(args[0])
			if err != nil {
				return err
			}
			at, err := model.ParseSeconds(args[1])
			if err != nil {
				return err
			}
			if err := refuseInsideFootage(p, args[2]); err != nil {
				return err
			}
			if err := frameGrab(abs, at, args[2]); err != nil {
				return err
			}
			fmt.Printf("frame %ss of %s -> %s\n", model.FormatSeconds(at), args[0], args[2])
			return nil
		},
	}
}
