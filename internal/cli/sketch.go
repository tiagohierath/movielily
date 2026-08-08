package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"milklily/internal/project"
)

func newSketchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sketch",
		Short: "Open Pictogrep storyboard mode for this project",
		Long: "sketch starts Pictogrep storyboard mode against refs/visual/ and writes\n" +
			"new drawings to storyboards/inbox/. It does not change any sequence; run\n" +
			"milklily intake boards <sequence> when the boards are ready to edit.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			binary := os.Getenv("MILKLILY_PICTOGREP")
			if binary == "" {
				binary, err = exec.LookPath("pictogrep")
				if err != nil {
					return fmt.Errorf("pictogrep not found (install it or set MILKLILY_PICTOGREP)")
				}
			}
			child := exec.Command(binary, "storyboard", "--project")
			child.Dir = p.Root
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				return err
			}
			fmt.Println("Pictogrep opened; when done, run: milklily intake boards main")
			return nil
		},
	}
}
