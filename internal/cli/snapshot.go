package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"movielily/internal/project"
)

// Snapshots are optional git integration: the project's plain-text
// instructions (config, markers, selects, notes, sequences) are committed to
// a git repo at the project root, so every cut of the movie can be revisited.
// Footage never enters git: it's read-only source material, huge, and already
// safe on disk; .gitignore keeps it (and exported files) out.

func newSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshot [message...]",
		Aliases: []string{"snap"},
		Short:   "Version the project's instructions with git (optional)",
		Long: "snapshot commits the project's plain-text instructions (config, markers,\n" +
			"selects, notes, sequences) to a git repository at the project root, creating\n" +
			"it on first use. Footage and exports are ignored, only the small text files\n" +
			"are versioned. Entirely optional: nothing else in movielily needs git.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return takeSnapshot(joinArgs(args))
		},
	}
	cmd.AddCommand(newSnapshotListCmd(), newSnapshotRestoreCmd())
	return cmd
}

func newSnapshotListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List snapshots, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if !hasSnapshotRepo(p.Root) {
				fmt.Println("no snapshots yet (take one with 'movielily snapshot')")
				return nil
			}
			out, err := git(p.Root, "log", "--pretty=format:%h  %ad  %s", "--date=format:%Y-%m-%d %H:%M")
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
}

func newSnapshotRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <snapshot>",
		Short: "Restore the instructions from a snapshot (by its short id)",
		Long: "restore brings every versioned file back to how it was in the given\n" +
			"snapshot. The current state is snapshotted first, so a restore can always\n" +
			"be undone by restoring the snapshot it just took. Footage is untouched.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if !hasSnapshotRepo(p.Root) {
				return fmt.Errorf("no snapshots yet (take one with 'movielily snapshot')")
			}
			ref := args[0]
			if _, err := git(p.Root, "rev-parse", "--verify", ref+"^{commit}"); err != nil {
				return fmt.Errorf("no such snapshot: %s (see 'movielily snapshot list')", ref)
			}
			// Safety net: the state being replaced becomes a snapshot itself.
			if err := takeSnapshot("before restoring " + ref); err != nil {
				return err
			}
			if _, err := git(p.Root, "checkout", ref, "--", "."); err != nil {
				return err
			}
			// Files that exist now but not in the snapshot survive a plain
			// checkout; committing the restored state keeps history linear and
			// makes 'snapshot list' tell the truth about what's on disk.
			if err := takeSnapshot("restore " + ref); err != nil {
				return err
			}
			fmt.Printf("restored snapshot %s\n", ref)
			return nil
		},
	}
}

func takeSnapshot(message string) error {
	p, err := project.Open()
	if err != nil {
		return err
	}
	if !hasSnapshotRepo(p.Root) {
		if err := initSnapshotRepo(p.Root); err != nil {
			return err
		}
		fmt.Println("initialized snapshot repository (footage/ and exports stay out of git)")
	}
	if _, err := git(p.Root, "add", "-A"); err != nil {
		return err
	}
	if out, _ := git(p.Root, "status", "--porcelain"); out == "" {
		fmt.Println("nothing changed since the last snapshot")
		return nil
	}
	if message == "" {
		message = "snapshot " + time.Now().Format("2006-01-02 15:04")
	}
	if _, err := git(p.Root, commitArgs(p.Root, message)...); err != nil {
		return err
	}
	id, _ := git(p.Root, "rev-parse", "--short", "HEAD")
	fmt.Printf("snapshot %s: %s\n", id, message)
	return nil
}

func hasSnapshotRepo(root string) bool {
	st, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && st.IsDir()
}

func initSnapshotRepo(root string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not found on PATH (snapshots need git; everything else works without it)")
	}
	if _, err := git(root, "init", "--quiet"); err != nil {
		return err
	}
	ignore := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		content := "# movielily: version the instructions, never the media\n" +
			"footage/\n" +
			"*.mp4\n*.mov\n*.mkv\n*.webm\n" +
			"# regenerable: review playlists and rendered title-card cache\n" +
			"*.review.edl\n" +
			".cache/\n"
		if err := os.WriteFile(ignore, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// commitArgs builds the commit command, supplying a local identity when the
// user has none configured so a first snapshot never fails on a fresh machine.
func commitArgs(root string, message string) []string {
	args := []string{}
	if email, _ := git(root, "config", "user.email"); email == "" {
		args = append(args, "-c", "user.name=movielily", "-c", "user.email=movielily@localhost")
	}
	return append(args, "commit", "--quiet", "-m", message)
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return "", fmt.Errorf("git %s: %s", args[0], s)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return s, nil
}
