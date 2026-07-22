package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/grade"
	"movielily/internal/project"
)

// The grade command manages reusable colour-grade / film-grain presets, stored
// as plain grades/*.grade text files. Applying a grade to a scene is done in
// the scene's note (inline params, or a #grade:name tag) — reversible and
// reproducible, never touching footage. See `movielily grade params`.
func newGradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "grade",
		Aliases: []string{"color", "colour"},
		Short:   "Manage colour-grade / film-grain presets (text, reversible)",
	}
	cmd.AddCommand(newGradeParamsCmd(), newGradeListCmd(), newGradeShowCmd(), newGradeSetCmd())
	return cmd
}

func newGradeParamsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "params",
		Short: "List the grade parameters and their ranges",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("grade parameters (neutral = no change):")
			for _, l := range grade.Help() {
				fmt.Println(l)
			}
			fmt.Println("\napply to a scene, two ways (both plain text in the sequence):")
			fmt.Println("  inline in the note:   video|clip.mp4|0|5|sunset saturation=120 grain=25")
			fmt.Println("  a named preset:       tag the note #grade:filmic (see 'grade set')")
			return nil
		},
	}
}

func newGradeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the grade presets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			entries, err := os.ReadDir(p.GradesDir())
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("no grade presets yet (create one with 'grade set <name> ...')")
					return nil
				}
				return err
			}
			found := false
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".grade") {
					name := strings.TrimSuffix(e.Name(), ".grade")
					g, err := grade.Load(p.GradesDir(), name)
					if err != nil {
						return err
					}
					fmt.Printf("%-16s %s\n", name, g.String())
					found = true
				}
			}
			if !found {
				fmt.Println("no grade presets yet (create one with 'grade set <name> ...')")
			}
			return nil
		},
	}
}

func newGradeShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a preset's values",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			g, err := grade.Load(p.GradesDir(), args[0])
			if err != nil {
				return err
			}
			if g.IsNeutral() {
				fmt.Printf("%s is neutral (no changes)\n", args[0])
				return nil
			}
			fmt.Printf("%s: %s\n", args[0], g.String())
			return nil
		},
	}
}

func newGradeSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <key=value>...",
		Short: "Create or edit a preset (e.g. grade set filmic saturation=115 grain=20 warmth=15)",
		Long: "set writes grades/<name>.grade, updating the given parameters and keeping\n" +
			"the rest. Set a parameter to its neutral value to remove it. Tag a scene's\n" +
			"note with #grade:<name> to apply it.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			name := args[0]
			// Start from the existing preset if any, so edits are incremental.
			g, err := grade.Load(p.GradesDir(), name)
			if err != nil {
				g = grade.New()
			}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("expected key=value, got %q", kv)
				}
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					return fmt.Errorf("%q is not a number", v)
				}
				if err := g.Set(k, f); err != nil {
					return err
				}
			}
			if err := grade.Save(p.GradesDir(), name, g); err != nil {
				return err
			}
			fmt.Printf("saved %s: %s\n", filepath.Join("grades", name+".grade"), g.String())
			fmt.Printf("apply it by tagging a scene's note: #grade:%s\n", name)
			return nil
		},
	}
}
