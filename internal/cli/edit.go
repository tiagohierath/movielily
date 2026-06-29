package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/project"
	"movielily/internal/tui"
)

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit [sequence]",
		Short: "Open a sequence in the interactive TUI editor (vim keys, frame preview)",
		Long: "edit opens a sequence — the project's edit-decision list — in an\n" +
			"interactive terminal UI: navigate with j/k, reorder with J/K, mark with\n" +
			"space, edit a scene's note (and #tags) with e, group scenes under section\n" +
			"headers with o, and preview each scene's first and last frame inline\n" +
			"(kitty graphics). A missing sequence is seeded from the project's selects.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			} else {
				name, err = soleSequence(p)
				if err != nil {
					return err
				}
			}
			return tui.Edit(p, name)
		},
	}
}

// soleSequence picks the obvious sequence when none is named: the only one that
// exists, or a sensible default for a fresh project.
func soleSequence(p *project.Project) (string, error) {
	entries, err := os.ReadDir(p.SequencesDir())
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var seqs []string
	for _, en := range entries {
		n := en.Name()
		if en.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".txt") {
			continue
		}
		seqs = append(seqs, strings.TrimSuffix(n, ".txt"))
	}
	switch len(seqs) {
	case 0:
		return "roughcut", nil
	case 1:
		return seqs[0], nil
	default:
		return "", fmt.Errorf("multiple sequences (%s) — name one: movielily edit <name>", strings.Join(seqs, ", "))
	}
}
