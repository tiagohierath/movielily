package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"milklily/internal/project"
)

func newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check and repair a milklily project workspace",
		Long: "doctor checks whether the current project is easy to maintain: standard\n" +
			"folders exist, text instructions are present, media is ignored by git,\n" +
			"and the repo is ready to push to GitHub. Use --fix to recreate missing\n" +
			"scaffold files without touching existing media or edits.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if fix {
				if err := p.EnsureStructure(); err != nil {
					return err
				}
			}
			return printDoctor(p, fix)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "create missing folders, helper READMEs, main sequence and .gitignore block")
	return cmd
}

func printDoctor(p *project.Project, fixed bool) error {
	fmt.Printf("project: %s\n", p.Config.Name)
	fmt.Printf("root:    %s\n\n", p.Root)

	missing := missingProjectPaths(p)
	if len(missing) == 0 {
		fmt.Println("structure: ok")
	} else {
		fmt.Printf("structure: missing %d path(s)\n", len(missing))
		for _, rel := range missing {
			fmt.Printf("  - %s\n", rel)
		}
		if !fixed {
			fmt.Println("  run: milklily doctor --fix")
		}
	}

	ok, err := hasMilklilyGitignore(p.Root)
	if err != nil {
		return err
	}
	if ok {
		fmt.Println("gitignore: ok, media and exports stay out of git")
	} else {
		fmt.Println("gitignore: missing milklily block")
		if !fixed {
			fmt.Println("  run: milklily doctor --fix")
		}
	}

	if hasSnapshotRepo(p.Root) {
		if tracked, err := trackedMedia(p); err != nil {
			return err
		} else if len(tracked) > 0 {
			fmt.Printf("git:       warning: %d media file(s) are already tracked\n", len(tracked))
			fmt.Println("  media stays on disk, but remove it from Git before sharing: git rm --cached <file>")
		}
		branch, _ := git(p.Root, "branch", "--show-current")
		if branch == "" {
			branch = "(detached)"
		}
		remote, _ := git(p.Root, "remote", "get-url", "origin")
		if remote == "" {
			fmt.Printf("git:       repo on %s, no origin remote yet\n", branch)
			fmt.Println("  add: git remote add origin git@github.com:USER/REPO.git")
		} else {
			fmt.Printf("git:       repo on %s, origin %s\n", branch, remote)
		}
	} else {
		fmt.Println("git:       not initialized")
		fmt.Println("  run: milklily snapshot \"first cut\"")
	}

	seqs, err := countSequenceFiles(p.SequencesDir())
	if err != nil {
		return err
	}
	fmt.Printf("sequences: %d text cut(s)\n", seqs)

	counts, err := countMediaByFolder(p)
	if err != nil {
		return err
	}
	fmt.Println("media:")
	for _, name := range []string{"storyboards", "images", "refs", "audio", "fxs", "footage"} {
		fmt.Printf("  %-11s %d file(s)\n", name+"/", counts[name])
	}
	return nil
}

func trackedMedia(p *project.Project) ([]string, error) {
	var paths []string
	for _, dir := range p.MediaDirs() {
		rel, ok := p.RelPath(dir)
		if ok {
			paths = append(paths, rel)
		}
	}
	out, err := git(p.Root, append([]string{"ls-files", "--"}, paths...)...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

func missingProjectPaths(p *project.Project) []string {
	var missing []string
	for _, path := range append(p.ProjectDirs(), p.ScaffoldFiles()...) {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if rel, ok := p.RelPath(path); ok {
			missing = append(missing, rel)
		} else {
			missing = append(missing, path)
		}
	}
	if n, err := countSequenceFiles(p.SequencesDir()); err == nil && n == 0 {
		missing = append(missing, filepath.ToSlash(filepath.Join("sequences", "main.txt")))
	}
	return missing
}

func hasMilklilyGitignore(root string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return strings.Contains(string(body), "# >>> milklily"), nil
}

func countSequenceFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var n int
	for _, e := range entries {
		if !e.IsDir() && project.IsSequenceFileName(e.Name()) {
			n++
		}
	}
	return n, nil
}

func countMediaByFolder(p *project.Project) (map[string]int, error) {
	counts := map[string]int{}
	for _, dir := range p.MediaDirs() {
		name := filepath.Base(dir)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.EqualFold(d.Name(), "README.txt") {
				return nil
			}
			if isMedia(d.Name()) {
				counts[name]++
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return counts, err
		}
	}
	return counts, nil
}
