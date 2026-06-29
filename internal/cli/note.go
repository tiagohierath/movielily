package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "note",
		Aliases: []string{"notes", "n"},
		Short:   "Add or list notes (searchable observations; use #tags)",
	}
	cmd.AddCommand(newNoteAddCmd(), newNoteListCmd())
	return cmd
}

func newNoteAddCmd() *cobra.Command {
	var clip, ts string
	cmd := &cobra.Command{
		Use:   "add <text...>",
		Short: "Add a note, optionally anchored to a clip and time",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			n := model.Note{Text: joinArgs(args)}
			if clip != "" {
				n.File = p.StoreName(clip)
			}
			if ts != "" {
				t, err := model.ParseSeconds(ts)
				if err != nil {
					return err
				}
				n.Time, n.HasTime = t, true
			}
			if err := store.Append(p.Notes(), n.String()); err != nil {
				return err
			}
			fmt.Printf("note added: %s\n", n.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&clip, "clip", "", "anchor the note to a clip")
	cmd.Flags().StringVar(&ts, "time", "", "anchor the note to a time (seconds or mm:ss)")
	return cmd
}

func newNoteListCmd() *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			notes, err := store.LoadNotes(p.Notes())
			if err != nil {
				return err
			}
			n := 0
			for _, note := range notes {
				if !matches(search, note.Text, note.File) {
					continue
				}
				fmt.Println(formatNote(note))
				n++
			}
			if n == 0 {
				fmt.Println("no notes")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "only notes matching this term")
	return cmd
}
