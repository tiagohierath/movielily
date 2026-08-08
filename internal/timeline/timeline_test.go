package timeline

import (
	"strings"
	"testing"

	"movielily/internal/model"
)

func TestResolveAnchorsBedToStillImage(t *testing.T) {
	items := []model.SequenceItem{
		{Kind: model.KindImage, File: "one.png", Dur: 2},
		{Kind: model.KindImage, File: "two.png", Dur: 3},
		{Kind: model.KindImage, File: "three.png", Dur: 1},
		{Kind: model.KindAudio, File: "voice.mp3", Note: "voice #at_image_3"},
	}
	pl, _, err := Resolve(t.TempDir(), items)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := model.TagNumber(pl.Beds[0].Note, "at"); !ok || got != 5 {
		t.Fatalf("resolved #at = %v, %v; want 5, true (%q)", got, ok, pl.Beds[0].Note)
	}
}

func TestResolveAnchorsSceneAndHonorsFixedTime(t *testing.T) {
	items := []model.SequenceItem{
		{Kind: model.KindImage, File: "one.png", Dur: 2},
		{Kind: model.KindVideo, File: "two.mp4", In: 0, Out: 4},
		{Kind: model.KindAudio, File: "scene.mp3", Note: "#at_scene_2"},
		{Kind: model.KindAudio, File: "fixed.mp3", Note: "#at_image_2 #at_9"},
	}
	pl, _, err := Resolve(t.TempDir(), items)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := model.TagNumber(pl.Beds[0].Note, "at"); got != 2 {
		t.Fatalf("scene anchor = %v, want 2", got)
	}
	if got, _ := model.TagNumber(pl.Beds[1].Note, "at"); got != 9 {
		t.Fatalf("fixed anchor = %v, want 9", got)
	}
}

func TestResolveRejectsMissingImageAnchor(t *testing.T) {
	_, _, err := Resolve(t.TempDir(), []model.SequenceItem{
		{Kind: model.KindImage, File: "one.png", Dur: 1},
		{Kind: model.KindAudio, File: "voice.mp3", Note: "#at_image_2"},
	})
	if err == nil || !strings.Contains(err.Error(), "beyond the 1 still images") {
		t.Fatalf("Resolve error = %v, want missing image anchor error", err)
	}
}
