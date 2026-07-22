package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
	"movielily/internal/typst"
)

func newSeqCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "seq",
		Aliases: []string{"sequence", "s"},
		Short:   "Build and inspect sequences",
	}
	cmd.AddCommand(newSeqListCmd(), newSeqShowCmd(), newSeqVideoCmd(), newSeqImageCmd(),
		newSeqAudioCmd(), newSeqTitleCmd(), newSeqAnimCmd(), newSeqOverlayCmd(),
		newSeqUseCmd(), newSeqFromSelectsCmd())
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
				case model.KindAudio:
					fmt.Printf("%2d. audio  %-20s %sdB (bed, under the whole cut)  %s\n", i+1, it.File, model.FormatSeconds(it.Gain), it.Note)
				case model.KindTitle:
					fmt.Printf("%2d. title  %-20s %ss  \"%s\"\n", i+1, it.File, model.FormatSeconds(it.Dur), it.Note)
				case model.KindAnim:
					fmt.Printf("%2d. anim   %-20s %ss  \"%s\"\n", i+1, it.File, model.FormatSeconds(it.Dur), it.Note)
				case model.KindOverlay:
					fmt.Printf("%2d. over   %-20s at +%ss for %ss @ %s  %s\n", i+1, it.File, model.FormatSeconds(it.In), model.FormatSeconds(it.Dur), it.Place, it.Note)
				case model.KindUse:
					fmt.Printf("%2d. use    %-20s (spliced in here)  %s\n", i+1, it.File, it.Note)
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

func newSeqAudioCmd() *cobra.Command {
	var gain float64
	cmd := &cobra.Command{
		Use:   "audio <sequence> <file> [note...]",
		Short: "Append a background audio bed (music/narration under the whole cut)",
		Long: "audio adds a background bed: the file plays from the start of the cut,\n" +
			"mixed under the clips' own sound, and stops when the video ends. Use --gain\n" +
			"to sit music below the voice (e.g. --gain -12). Beds occupy no timeline\n" +
			"slot; both export and review play them.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if _, err := p.ResolveFootage(args[1]); err != nil {
				return err
			}
			it := model.SequenceItem{Kind: model.KindAudio, File: p.StoreName(args[1]), Gain: gain, Note: joinArgs(args[2:])}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
	cmd.Flags().Float64Var(&gain, "gain", 0, "bed level in dB (negative sits it under the voice, e.g. -12)")
	return cmd
}

func newSeqTitleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "title <sequence> <template> <duration> <text...>",
		Short: "Append a generated title card (typst template + your text)",
		Long: "title inserts a card rendered from a typst template in titles/, shown\n" +
			"full-frame for the duration. The template is the reusable STYLE; the text\n" +
			"here is what this one card says. First use creates titles/chapter.typ to\n" +
			"start from. Edit a card's text later with 'e' in the TUI or in the file.",
		Args: cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if created, err := typst.EnsureDefault(p); err != nil {
				return err
			} else if created != "" {
				fmt.Printf("created %s (the default card style; edit it freely)\n",
					filepath.Join(typst.TitlesDir(p), created))
			}
			dur, err := model.ParseSeconds(args[2])
			if err != nil {
				return err
			}
			if dur <= 0 {
				return fmt.Errorf("duration must be positive")
			}
			text := joinArgs(args[3:])
			it := model.SequenceItem{Kind: model.KindTitle, File: typst.StoreName(args[1]), Dur: dur, Note: text}
			// Render now: catches a typo'd template or a typst error at add
			// time instead of at export time, and warms the cache.
			if _, err := typst.Render(p, it.File, it.Note); err != nil {
				return err
			}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
	return cmd
}

func newSeqAnimCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "anim <sequence> <template> <text...>",
		Short: "Append an animated card (manim template + your text)",
		Long: "anim inserts an animated card rendered from a manim scene in anims/ (the\n" +
			"scene class must be named Card; it reads its text from $MOVIELILY_TEXT).\n" +
			"movielily renders it at the project's frame and fps, measures its length,\n" +
			"and caches it, so each template+text pair renders once. First use creates\n" +
			"anims/card.py to start from.",
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if created, err := manim.EnsureDefault(p); err != nil {
				return err
			} else if created != "" {
				fmt.Printf("created %s (the default animated card; edit it freely)\n",
					filepath.Join(manim.AnimsDir(p), created))
			}
			text := joinArgs(args[2:])
			name := manim.StoreName(args[1])
			fmt.Println("rendering animated card (first time for this template+text is slow)…")
			clip, err := manim.Render(p, name, text)
			if err != nil {
				return err
			}
			dur, err := manim.Probe(clip)
			if err != nil {
				return err
			}
			it := model.SequenceItem{Kind: model.KindAnim, File: name, Dur: round2(dur), Note: text}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
	return cmd
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func newSeqOverlayCmd() *cobra.Command {
	var place string
	cmd := &cobra.Command{
		Use:   "overlay <sequence> <image> <at> <duration> [note...]",
		Short: "Overlay an image on the LAST scene of the sequence",
		Long: "overlay puts an image on top of the sequence's last scene, appearing <at>\n" +
			"seconds into it for <duration> seconds (0 = until the scene ends). --place\n" +
			"positions it: tl/tr/bl/br/c plus a width percent (br:33 = bottom-right,\n" +
			"a third of the frame) or full. In the TUI, overlays ride the scene above\n" +
			"them; move the scene and its overlays travel along.",
		Args: cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			// An overlay is an image from footage/ OR a typst template from
			// titles/ (its text = the note): reusable lower-thirds/captions.
			if strings.HasSuffix(args[1], ".typ") {
				if _, err := typst.Resolve(p, args[1]); err != nil {
					return err
				}
			} else if _, err := p.ResolveFootage(args[1]); err != nil {
				return err
			}
			at, err := model.ParseSeconds(args[2])
			if err != nil {
				return err
			}
			dur, err := model.ParseSeconds(args[3])
			if err != nil {
				return err
			}
			if _, _, err := model.ParsePlace(place); err != nil {
				return err
			}
			file := p.StoreName(args[1])
			if strings.HasSuffix(args[1], ".typ") {
				file = typst.StoreName(args[1])
			}
			it := model.SequenceItem{Kind: model.KindOverlay, File: file,
				In: at, Dur: dur, Place: place, Note: joinArgs(args[4:])}
			if err := store.Append(p.Sequence(args[0]), it.String()); err != nil {
				return err
			}
			fmt.Printf("added to %s: %s\n", args[0], it.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&place, "place", model.DefaultPlace, "position: tl/tr/bl/br/c[:width%] or full")
	return cmd
}

func newSeqUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <sequence> <other-sequence>",
		Short: "Splice another sequence in at this point (nested sequences)",
		Long: "use appends a use|other record: on review and export the other sequence's\n" +
			"items play here, so a long film assembles from per-chapter sequences that\n" +
			"are each edited on their own.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			name := strings.TrimSuffix(filepath.Base(args[1]), ".txt")
			if name == strings.TrimSuffix(filepath.Base(args[0]), ".txt") {
				return fmt.Errorf("a sequence cannot use itself")
			}
			if _, err := os.Stat(p.Sequence(name)); err != nil {
				return fmt.Errorf("sequence %q does not exist yet", name)
			}
			it := model.SequenceItem{Kind: model.KindUse, File: name}
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
