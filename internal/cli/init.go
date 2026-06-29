package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/project"
)

func newInitCmd() *cobra.Command {
	var name, footageSrc string
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create a new movielily project (a folder with footage/ inside)",
		Long: "Create a new movielily project. `movielily init my-film` makes a my-film/\n" +
			"folder containing footage/, sequences/ and the log files. Drop your clips\n" +
			"and images into footage/ (or use --footage to copy an existing folder in).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			p, err := project.Init(dir, name)
			if err != nil {
				return err
			}
			fmt.Printf("created project %q in %s\n", p.Config.Name, p.Root)

			if footageSrc != "" {
				n, err := importFootage(footageSrc, p.Footage())
				if err != nil {
					return err
				}
				fmt.Printf("copied %d media file(s) into %s\n", n, p.Footage())
			}

			fmt.Printf("\nput footage here:  %s\n", p.Footage())
			fmt.Println("then:              movielily watch <clip>")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (defaults to directory name)")
	cmd.Flags().StringVar(&footageSrc, "footage", "", "copy media files from this directory into the new project's footage/")
	return cmd
}

// importFootage copies (never moves) media files from src into dst, keeping the
// originals intact: source footage + instructions = export.
func importFootage(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !isMedia(e.Name()) {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func isMedia(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".mkv", ".m4v", ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
