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
	var initGit bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create a new movielily short-film project",
		Long: "Create a new movielily project. `movielily init my-film` makes a my-film/\n" +
			"folder with clear short-film areas: scripts/, refs/, storyboards/, images/,\n" +
			"audio/, fxs/, footage/, sequences/ and exports/. Add --git to make the\n" +
			"project immediately GitHub-ready: instructions committed, media ignored.",
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
				n, err := importFootage(footageSrc, p.FootageRawDir())
				if err != nil {
					return err
				}
				fmt.Printf("copied %d media file(s) into %s\n", n, p.FootageRawDir())
			}
			if initGit {
				if err := takeSnapshotProject(p, "initial movielily project"); err != nil {
					return err
				}
				fmt.Println("git ready: add a GitHub remote and push")
			}

			fmt.Println("\nproject folders:")
			fmt.Printf("  scripts/      writing, scene notes, dialogue drafts\n")
			fmt.Printf("  refs/         reference images\n")
			fmt.Printf("  storyboards/  board drawings and animatic stills\n")
			fmt.Printf("  images/       cleaned/key stills for the cut\n")
			fmt.Printf("  audio/        dialogue, narration, music, ambience\n")
			fmt.Printf("  fxs/          overlays, textures, effect plates\n")
			fmt.Printf("  footage/      raw clips and legacy catch-all media\n")
			fmt.Printf("  sequences/    plain-text cuts\n")
			fmt.Printf("  exports/      rendered movies and PDFs\n")
			fmt.Println("\nthen: movielily board main --open")
			fmt.Println("or:   movielily edit main")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (defaults to directory name)")
	cmd.Flags().StringVar(&footageSrc, "footage", "", "copy media files from this directory into the new project's footage/raw/")
	cmd.Flags().BoolVar(&initGit, "git", false, "initialize git and commit the text project scaffold")
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
	case ".mp4", ".mov", ".mkv", ".m4v", ".webm", ".jpg", ".jpeg", ".png", ".webp", ".wav", ".mp3", ".m4a", ".flac", ".aac", ".ogg":
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
