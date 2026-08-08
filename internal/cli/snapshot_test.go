package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"movielily/internal/project"
)

func TestSnapshotStagesOnlyPortableProjectInstructions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	p, err := project.Init(filepath.Join(t.TempDir(), "film"), "film")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.StoryboardInboxDir(), "source.png"), []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, "private-scratch.txt"), []byte("not project state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := takeSnapshotProject(p, "first cut"); err != nil {
		t.Fatal(err)
	}
	tracked, err := git(p.Root, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"storyboards/inbox/source.png", "private-scratch.txt"} {
		if strings.Contains(tracked, forbidden) {
			t.Fatalf("snapshot tracked %q:\n%s", forbidden, tracked)
		}
	}
	if !strings.Contains(tracked, "sequences/main.txt") || !strings.Contains(tracked, "movielily.conf") {
		t.Fatalf("snapshot missed project instructions:\n%s", tracked)
	}
}
