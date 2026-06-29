package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <term...>",
		Short: "Search markers, selects and notes",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			term := joinArgs(args)

			markers, err := store.LoadMarkers(p.Markers())
			if err != nil {
				return err
			}
			selects, err := store.LoadSelects(p.Selects())
			if err != nil {
				return err
			}
			notes, err := store.LoadNotes(p.Notes())
			if err != nil {
				return err
			}

			var mk, sl, nt []string
			for _, m := range markers {
				if matches(term, m.Note, m.File) {
					mk = append(mk, fmt.Sprintf("%-20s %ss  %s", m.File, model.FormatSeconds(m.Time), m.Note))
				}
			}
			for _, s := range selects {
				if matches(term, s.Note, s.File) {
					sl = append(sl, fmt.Sprintf("%-20s %s–%s  %s", s.File, model.FormatSeconds(s.In), model.FormatSeconds(s.Out), s.Note))
				}
			}
			for _, n := range notes {
				if matches(term, n.Text, n.File) {
					nt = append(nt, formatNote(n))
				}
			}

			total := len(mk) + len(sl) + len(nt)
			if total == 0 {
				fmt.Printf("no matches for %q\n", term)
				return nil
			}
			printSection("markers", mk)
			printSection("selects", sl)
			printSection("notes", nt)
			fmt.Printf("\n%d match(es)\n", total)
			return nil
		},
	}
}
