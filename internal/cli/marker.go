package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newMarkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "marker",
		Aliases: []string{"markers", "m"},
		Short:   "Add or list markers (points in time worth revisiting)",
	}
	cmd.AddCommand(newMarkerAddCmd(), newMarkerListCmd())
	return cmd
}

func newMarkerAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <clip> <time> [note...]",
		Short: "Add a marker",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			t, err := model.ParseSeconds(args[1])
			if err != nil {
				return err
			}
			m := model.Marker{File: p.StoreName(args[0]), Time: t, Note: joinArgs(args[2:])}
			if err := store.Append(p.Markers(), m.String()); err != nil {
				return err
			}
			fmt.Printf("marker added: %s\n", m.String())
			return nil
		},
	}
}

func newMarkerListCmd() *cobra.Command {
	var search, clip string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List markers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			markers, err := store.LoadMarkers(p.Markers())
			if err != nil {
				return err
			}
			n := 0
			for _, m := range markers {
				if clip != "" && m.File != clip {
					continue
				}
				if !matches(search, m.Note, m.File) {
					continue
				}
				fmt.Printf("%-20s %8ss  %s\n", m.File, model.FormatSeconds(m.Time), m.Note)
				n++
			}
			if n == 0 {
				fmt.Println("no markers")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "only markers matching this term")
	cmd.Flags().StringVar(&clip, "clip", "", "only markers for this clip")
	return cmd
}
