package mpv

import (
	"os"
	"os/exec"
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

// Gain tags, mute windows, bed placement and overlays all reshape the review
// graph; mpv itself validates the syntax when available.
func TestReviewArgsEssayFeatures(t *testing.T) {
	p := testProject(t, "a.mp4", "b.mp4", "ref.png", "song.mp3")
	items := []model.SequenceItem{
		{Kind: model.KindVideo, File: "a.mp4", In: 0, Out: 2, Note: "loud #-6db"},
		{Kind: model.KindOverlay, File: "ref.png", In: 0.5, Dur: 1, Place: "tr:30"},
		{Kind: model.KindVideo, File: "b.mp4", In: 0, Out: 2, Note: "broll #mute"},
		{Kind: model.KindAudio, File: "song.mp3", Gain: -12, Note: "#at_1 #from_30 #for_2"},
	}
	args, _, _, _, err := reviewArgs(p, "cut", items, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := find(t, args, "--lavfi-complex=")
	for _, want := range []string{
		"volume=-6dB:enable='between(t,0,2)'",                                       // gain tag window
		"volume=0:enable='between(t,2,4)'",                                          // mute window
		"[vid1]scale=1440:1080",                                                     // framing inside the graph
		"overlay=x=main_w-overlay_w-24:y=24:enable='between(t,0.5,1.5)'",            // composited overlay
		"atrim=start=30:end=32,asetpts=PTS-STARTPTS,adelay=1000:all=1,volume=-12dB", // bed placement
		"null[vo]",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("graph missing %q:\n%s", want, g)
		}
	}
	find(t, args, "--external-file=")
	for _, a := range args {
		if strings.HasPrefix(a, "--vf=") {
			t.Errorf("plain --vf should be absent when overlays join the graph: %s", a)
		}
	}
}

// The generated graphs must be syntactically valid for real mpv, not just for
// our expectations. Empty files decode as nothing, so force a quick exit; a
// graph parse error still fails the run.
func TestReviewArgsAcceptedByMpv(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	p := testProject(t, "a.mp4", "ref.png", "song.mp3")
	items := []model.SequenceItem{
		{Kind: model.KindVideo, File: "a.mp4", In: 0, Out: 1, Note: "#-3db"},
		{Kind: model.KindOverlay, File: "ref.png", In: 0, Dur: 1, Place: "br:25"},
		{Kind: model.KindAudio, File: "song.mp3", Gain: -10, Note: "#at_0.5"},
	}
	args, _, _, _, err := reviewArgs(p, "cut", items, 0)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, "--no-config", "--vo=null", "--ao=null", "--frames=1", "--end=0.1")
	out, _ := exec.Command("mpv", args...).CombinedOutput()
	for _, bad := range []string{"error parsing", "Cannot create filter", "Invalid", "failed to configure"} {
		if strings.Contains(strings.ToLower(string(out)), strings.ToLower(bad)) {
			t.Fatalf("mpv rejected the graph: %s\nargs: %v", out, args)
		}
	}
}
