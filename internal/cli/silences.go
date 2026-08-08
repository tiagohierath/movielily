package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"milklily/internal/ffmpeg"
	"milklily/internal/model"
	"milklily/internal/project"
	"milklily/internal/store"
)

// newSilencesCmd proposes selects from a continuous narration recording: the
// spoken stretches between silences become candidate takes. Print first,
// write with --keep, then prune the misfires in the TUI.
func newSilencesCmd() *cobra.Command {
	var noise, minGap, pad float64
	var keep bool
	cmd := &cobra.Command{
		Use:   "silences <audio>",
		Short: "Find the spoken parts of a recording (auto-selects around the pauses)",
		Long: "silences scans a narration recording and lists its NON-silent stretches:\n" +
			"record everything in one take, pauses and retakes included, and this finds\n" +
			"the speech. --keep appends the stretches to selects.txt (padded a little on\n" +
			"each side so words are never clipped); build a cut with 'seq from-selects'\n" +
			"and prune the misfires in the TUI.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			abs, err := p.ResolveFootage(args[0])
			if err != nil {
				return err
			}
			regions, total, err := ffmpeg.SpokenRegions(abs, noise, minGap)
			if err != nil {
				return err
			}
			if len(regions) == 0 {
				fmt.Printf("no speech found (the whole file sits under %.0fdB; try --noise %.0f)\n", noise, noise+10)
				return nil
			}
			stored := p.StoreName(args[0])
			existing, err := store.LoadSelects(p.Selects())
			if err != nil {
				return err
			}
			seen := make(map[string]bool, len(existing))
			for _, s := range existing {
				seen[silenceSelectKey(s)] = true
			}
			kept := 0
			skipped := 0
			var spoken float64
			for i, r := range regions {
				in := r[0] - pad
				if in < 0 {
					in = 0
				}
				out := r[1] + pad
				if out > total {
					out = total
				}
				spoken += out - in
				fmt.Printf("%2d. %8ss – %8ss  (%ss)\n", i+1,
					model.FormatSeconds(round1(in)), model.FormatSeconds(round1(out)), model.FormatSeconds(round1(out-in)))
				if keep {
					s := model.Select{File: stored, In: round1(in), Out: round1(out), Note: fmt.Sprintf("take %d", i+1)}
					key := silenceSelectKey(s)
					if seen[key] {
						skipped++
					} else {
						if err := store.Append(p.Selects(), s.String()); err != nil {
							return err
						}
						seen[key] = true
						kept++
					}
				}
			}
			fmt.Printf("\n%d spoken stretch(es), %ss of speech in a %ss recording\n",
				len(regions), model.FormatSeconds(round1(spoken)), model.FormatSeconds(round1(total)))
			if keep {
				fmt.Printf("added %d new select(s)", kept)
				if skipped > 0 {
					fmt.Printf(" · skipped %d already present", skipped)
				}
				fmt.Println(" · source audio unchanged · next: milklily seq from-selects <name>")
			} else {
				fmt.Println("nothing written · re-run with --keep to append these to selects.txt")
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&noise, "noise", -35, "silence floor in dB (lower = stricter about what counts as quiet)")
	cmd.Flags().Float64Var(&minGap, "gap", 0.6, "a pause must last this many seconds to split two takes")
	cmd.Flags().Float64Var(&pad, "pad", 0.15, "seconds kept around each stretch so words are never clipped")
	cmd.Flags().BoolVar(&keep, "keep", false, "append the stretches to selects.txt")
	return cmd
}

// silenceSelectKey identifies the meaningful part of a generated select. It
// makes --keep safe to repeat: the same audio region is never appended twice.
func silenceSelectKey(s model.Select) string {
	return fmt.Sprintf("%s|%.1f|%.1f", s.File, s.In, s.Out)
}
