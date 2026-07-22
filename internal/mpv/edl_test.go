package mpv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"movielily/internal/model"
	"movielily/internal/project"
)

// testProject builds a throwaway project whose footage/ holds empty files with
// the given names (Review only stats them; content never matters here).
func testProject(t *testing.T, files ...string) *project.Project {
	t.Helper()
	root := t.TempDir()
	foot := filepath.Join(root, "footage")
	if err := os.MkdirAll(foot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(foot, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sequences"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := project.DefaultConfig()
	return &project.Project{Root: root, Config: c}
}

func find(t *testing.T, args []string, prefix string) string {
	t.Helper()
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return a
		}
	}
	t.Fatalf("no arg with prefix %q in %v", prefix, args)
	return ""
}

// The common case: clips + a still + one bed. The graph must repair the audio
// holes stills leave (aresample+apad) and mix the bed under at its gain.
func TestReviewArgsClipsAndBed(t *testing.T) {
	p := testProject(t, "a.mp4", "b.mp4", "title.png", "song.mp3")
	items := []model.SequenceItem{
		{Kind: model.KindSection, Note: "Scene 1"},
		{Kind: model.KindVideo, File: "a.mp4", In: 1, Out: 3},
		{Kind: model.KindImage, File: "title.png", Dur: 3},
		{Kind: model.KindVideo, File: "b.mp4", In: 0, Out: 2},
		{Kind: model.KindAudio, File: "song.mp3", Gain: -12},
	}
	args, clips, stills, beds, err := reviewArgs(p, "cut", items, 0)
	if err != nil {
		t.Fatal(err)
	}
	if clips != 2 || stills != 1 || beds != 1 {
		t.Fatalf("counts = %d/%d/%d, want 2/1/1", clips, stills, beds)
	}
	g := find(t, args, "--lavfi-complex=")
	for _, want := range []string{
		"[aid1]aresample=async=1000:first_pts=0,apad=whole_dur=7[t]",
		"[aid2]volume=-12dB[b0]",
		"[t][b0]amix=inputs=2:duration=first:normalize=0[ao]",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph missing %q:\n%s", want, g)
		}
	}
	find(t, args, "--audio-file=")
	find(t, args, "--vf=lavfi=[scale=1440:1080")

	edl, err := os.ReadFile(args[0])
	if err != nil {
		t.Fatal(err)
	}
	s := string(edl)
	if !strings.Contains(s, "a.mp4,1,2") || !strings.Contains(s, "title.png,0,3") {
		t.Errorf("unexpected EDL:\n%s", s)
	}
}

// Starting mid-cut: the timeline before `from` is skipped and each bed is
// trimmed by exactly that offset so the music lands where the export puts it.
func TestReviewArgsFromOffset(t *testing.T) {
	p := testProject(t, "a.mp4", "b.mp4", "song.mp3")
	items := []model.SequenceItem{
		{Kind: model.KindVideo, File: "a.mp4", In: 1, Out: 3.5}, // 2.5s
		{Kind: model.KindVideo, File: "b.mp4", In: 0, Out: 2},
		{Kind: model.KindAudio, File: "song.mp3", Gain: -6},
	}
	args, clips, _, _, err := reviewArgs(p, "cut", items, 1)
	if err != nil {
		t.Fatal(err)
	}
	if clips != 1 {
		t.Fatalf("clips = %d, want 1 (timeline starts at item 1)", clips)
	}
	g := find(t, args, "--lavfi-complex=")
	if !strings.Contains(g, "atrim=start=2.5,asetpts=PTS-STARTPTS,volume=-6dB") {
		t.Errorf("bed not offset by 2.5s:\n%s", g)
	}
	if !strings.Contains(g, "apad=whole_dur=2[t]") {
		t.Errorf("remaining length should be 2s:\n%s", g)
	}
}

// A stills-only cut has no timeline audio track, so the bed becomes [aid1]
// and playback must be capped at the timeline's end.
func TestReviewArgsStillsOnly(t *testing.T) {
	p := testProject(t, "title.png", "song.mp3")
	items := []model.SequenceItem{
		{Kind: model.KindImage, File: "title.png", Dur: 4},
		{Kind: model.KindAudio, File: "song.mp3", Gain: 0},
	}
	args, _, stills, beds, err := reviewArgs(p, "cut", items, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stills != 1 || beds != 1 {
		t.Fatalf("counts %d stills / %d beds, want 1/1", stills, beds)
	}
	if g := find(t, args, "--lavfi-complex="); g != "--lavfi-complex=[aid1]volume=0dB[ao]" {
		t.Errorf("unexpected stills-only graph: %s", g)
	}
	if end := find(t, args, "--end="); end != "--end=4" {
		t.Errorf("playback not capped at timeline end: %s", end)
	}
}

// No beds: plain EDL playback, no filter graph at all.
func TestReviewArgsNoBeds(t *testing.T) {
	p := testProject(t, "a.mp4")
	items := []model.SequenceItem{{Kind: model.KindVideo, File: "a.mp4", In: 0, Out: 2}}
	args, _, _, beds, err := reviewArgs(p, "cut", items, 0)
	if err != nil {
		t.Fatal(err)
	}
	if beds != 0 {
		t.Fatalf("beds = %d, want 0", beds)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--lavfi-complex=") || strings.HasPrefix(a, "--audio-file=") {
			t.Errorf("unexpected audio arg without beds: %s", a)
		}
	}
}
