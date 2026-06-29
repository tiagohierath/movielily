package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newTagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag [name]",
		Short: "List #tags, or show everything tagged #name",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
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

			if len(args) == 0 {
				var texts []string
				for _, m := range markers {
					texts = append(texts, m.Note)
				}
				for _, s := range selects {
					texts = append(texts, s.Note)
				}
				for _, n := range notes {
					texts = append(texts, n.Text)
				}
				tags := model.AllTags(texts)
				if len(tags) == 0 {
					fmt.Println("no tags yet — add #tags to notes/markers/selects")
					return nil
				}
				for _, tc := range tags {
					fmt.Printf("%-18s %d\n", tc.Tag, tc.Count)
				}
				return nil
			}

			name := strings.ToLower(args[0])
			if !strings.HasPrefix(name, "#") {
				name = "#" + name
			}
			var mk, sl, nt []string
			for _, m := range markers {
				if hasTag(name, m.Note) {
					mk = append(mk, fmt.Sprintf("%-20s %ss  %s", m.File, model.FormatSeconds(m.Time), m.Note))
				}
			}
			for _, s := range selects {
				if hasTag(name, s.Note) {
					sl = append(sl, fmt.Sprintf("%-20s %s–%s  %s", s.File, model.FormatSeconds(s.In), model.FormatSeconds(s.Out), s.Note))
				}
			}
			for _, n := range notes {
				if hasTag(name, n.Text) {
					nt = append(nt, formatNote(n))
				}
			}
			total := len(mk) + len(sl) + len(nt)
			if total == 0 {
				fmt.Printf("nothing tagged %s\n", name)
				return nil
			}
			printSection("markers", mk)
			printSection("selects", sl)
			printSection("notes", nt)
			fmt.Printf("\n%d item(s) tagged %s\n", total, name)
			return nil
		},
	}
}
