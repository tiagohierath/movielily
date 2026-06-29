package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newSelectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "select",
		Aliases: []string{"selects", "sel"},
		Short:   "Add or list selects (clips with IN/OUT worth keeping)",
	}
	cmd.AddCommand(newSelectAddCmd(), newSelectListCmd())
	return cmd
}

func newSelectAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <clip> <in> <out> [note...]",
		Short: "Add a select",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			in, err := model.ParseSeconds(args[1])
			if err != nil {
				return err
			}
			out, err := model.ParseSeconds(args[2])
			if err != nil {
				return err
			}
			if out <= in {
				return fmt.Errorf("out (%s) must be greater than in (%s)", args[2], args[1])
			}
			s := model.Select{File: p.StoreName(args[0]), In: in, Out: out, Note: joinArgs(args[3:])}
			if err := store.Append(p.Selects(), s.String()); err != nil {
				return err
			}
			fmt.Printf("select added: %s\n", s.String())
			return nil
		},
	}
}

func newSelectListCmd() *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List selects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			selects, err := store.LoadSelects(p.Selects())
			if err != nil {
				return err
			}
			n := 0
			for _, s := range selects {
				if !matches(search, s.Note, s.File) {
					continue
				}
				fmt.Printf("%-20s %s–%s (%ss)  %s\n", s.File, model.FormatSeconds(s.In), model.FormatSeconds(s.Out), model.FormatSeconds(round1(s.Duration())), s.Note)
				n++
			}
			if n == 0 {
				fmt.Println("no selects")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "only selects matching this term")
	return cmd
}
