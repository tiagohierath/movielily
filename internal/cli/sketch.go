package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"movielily/internal/project"
)

func newSketchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sketch",
		Short: "Open Bildkasten storyboard mode for this project",
		Long: "sketch starts Bildkasten storyboard mode against refs/visual/ and writes\n" +
			"new drawings to storyboards/inbox/. It does not change any sequence; run\n" +
			"movielily intake boards <sequence> when the boards are ready to edit.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			binary := os.Getenv("MOVIELILY_BILDKASTEN")
			if binary == "" {
				binary, err = exec.LookPath("bildkasten")
				if err != nil {
					return fmt.Errorf("bildkasten not found (install it or set MOVIELILY_BILDKASTEN)")
				}
			}
			child := exec.Command(binary, "storyboard", "--project")
			child.Dir = p.Root
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				return err
			}
			fmt.Println("Bildkasten opened; when done, run: movielily intake boards main")
			return nil
		},
	}
}
