package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func newSeqCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "seq",
		Aliases: []string{"sequence", "s"},
		Short:   "Build and inspect sequences",
	}
	cmd.AddCommand(newSeqListCmd(), newSeqShowCmd(), newSeqVideoCmd(), newSeqImageCmd(), newSeqFromSelectsCmd())
	return cmd
}

func newSeqListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sequences",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			entries, err := os.ReadDir(p.SequencesDir())
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("no sequences yet")
					return nil
				}
				return err
			}
			found := false
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".txt") {
					continue
				}
				items, err := store.LoadSequence(filepath.Join(p.SequencesDir(), name))
				if err != nil {
					return err
				}
				var total float64
				for _, it := range items {
					total += it.Duration()
				}
				fmt.Printf("%-20s %3d item(s)  %ss\n", strings.TrimSuffix(name, ".txt"), len(items), model.FormatSeconds(round1(total)))
				found = true
			}
			if !found {
				fmt.Println("no sequences yet")
			}
			return nil
		},
	}
}

func newSeqShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <sequence>",
		Short: "Show the items in a sequence",
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
				fmt.Printf("sequence %q is empty or missing\n", args[0])
				return nil
			}
			var total float64
			for i, it := range items {
				switch it.Kind {
				case model.KindSection:
					fmt.Printf("--- %s\n", it.Note)
				case model.KindImage:
					fmt.Printf("%2d. image  %-20s %ss  %s\n", i+1, it.File, model.FormatSeconds(it.Dur), it.Note)
				default:
					fmt.Printf("%2d. video  %-20s %s–%s (%ss)  %s\n", i+1, it.File, model.FormatSeconds(it.In), model.FormatSeconds(it.Out), model.FormatSeconds(round1(it.Duration())), it.Note)
				}
				total += it.Duration()
			}
			fmt.Printf("    total: %ss\n", model.FormatSeconds(round1(total)))
			return nil
		},
	}
}

func newSeqVideoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "video <sequence> <clip> <in> <out> [note...]",
		Short: "Append a video clip to a sequence",
		Args:  cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			in, err := model.ParseSeconds(args[2])
			if err != nil {
				return err
			}
			out, err := model.ParseSeconds(args[3])
			if err != nil {
				return err
			}
			if out <= in {
				return fmt.Errorf("out (%s) must be greater than in (%s)", args[3], args[2])
			}
			it := model.SequenceItem{Kind: model.KindVideo, File: p.StoreName(args[1]), In: in, Out: out, Note: joinArgs(args[4:])}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
}

func newSeqImageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "image <sequence> <image> <duration> [note...]",
		Short: "Append a still image to a sequence",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			dur, err := model.ParseSeconds(args[2])
			if err != nil {
				return err
			}
			if dur <= 0 {
				return fmt.Errorf("duration must be positive")
			}
			it := model.SequenceItem{Kind: model.KindImage, File: p.StoreName(args[1]), Dur: dur, Note: joinArgs(args[3:])}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
}

func newSeqFromSelectsCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "from-selects <sequence>",
		Short: "Create a sequence from all current selects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			path := p.Sequence(args[0])
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("sequence %q already exists (use --force to overwrite)", args[0])
			}
			selects, err := store.LoadSelects(p.Selects())
			if err != nil {
				return err
			}
			if len(selects) == 0 {
				return fmt.Errorf("no selects to build from (selects.txt is empty)")
			}
			lines := []string{"# " + args[0] + " — generated from selects"}
			for _, s := range selects {
				lines = append(lines, s.AsItem().String())
			}
			if err := store.WriteLines(path, lines); err != nil {
				return err
			}
			fmt.Printf("wrote %s with %d item(s)\n", args[0], len(selects))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite if the sequence already exists")
	return cmd
}
