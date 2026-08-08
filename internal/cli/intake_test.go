package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

func TestIntakeImagesUsesBildkastenSidecarAndSkipsDuplicates(t *testing.T) {
	p, err := project.Init(filepath.Join(t.TempDir(), "film"), "film")
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(p.StoryboardInboxDir(), "0001_4x3_dusk.png")
	if err := os.WriteFile(image, []byte("not decoded by intake"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"tags":["cinematic","night"],"query":"moody dusk exterior"}`
	if err := os.WriteFile(strings.TrimSuffix(image, ".png")+".json", []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := intakeImages(p, "main", p.StoryboardInboxDir(), 48)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first intake = %d, want 1", n)
	}
	items, err := store.LoadSequence(p.Sequence("main"))
	if err != nil {
		t.Fatal(err)
	}
	var imageItem model.SequenceItem
	for _, item := range items {
		if item.Kind == model.KindImage {
			imageItem = item
		}
	}
	if imageItem.File != "storyboards/inbox/0001_4x3_dusk.png" {
		t.Fatalf("stored file = %q", imageItem.File)
	}
	if imageItem.Note != "moody dusk exterior #cinematic #night" {
		t.Fatalf("note = %q", imageItem.Note)
	}
	n, err = intakeImages(p, "main", p.StoryboardInboxDir(), 48)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second intake = %d, want 0", n)
	}
}
