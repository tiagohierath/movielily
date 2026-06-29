package mpv

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestIPCClientRoundTrip exercises the exact mechanism `watch` relies on: a
// command round-trips, and a broadcast script-message comes back as a
// client-message event (which is how a key press becomes a marker/select).
func TestIPCClientRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	sock := filepath.Join(t.TempDir(), "mpv.sock")
	cmd := exec.Command("mpv", "--no-video", "--idle=yes", "--really-quiet", "--input-ipc-server="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mpv: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	c, err := Dial(sock, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Command("get_version"); err != nil {
		t.Fatalf("get_version: %v", err)
	}

	if _, err := c.Command("script-message", "ml-marker"); err != nil {
		t.Fatalf("script-message: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-c.Events():
			if ev.Event == "client-message" && len(ev.Args) > 0 && ev.Args[0] == "ml-marker" {
				return // success: the watch capture path works
			}
		case <-deadline:
			t.Fatal("did not receive the ml-marker client-message event")
		}
	}
}

// TestSetChapters confirms in/out points can be pushed to mpv's seekbar at
// runtime — the feature `watch` uses to visualise markers and IN/OUT.
func TestSetChapters(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", clip)
	if err := gen.Run(); err != nil {
		t.Skipf("could not generate test clip: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "mpv.sock")
	cmd := exec.Command("mpv", "--vo=null", "--ao=null", "--idle=yes", "--really-quiet", "--pause", "--input-ipc-server="+sock, clip)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mpv: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	c, err := Dial(sock, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Wait for the file to finish loading (duration becomes available).
	loaded := false
	for i := 0; i < 50; i++ {
		if _, err := c.Command("get_property", "duration"); err == nil {
			loaded = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !loaded {
		t.Fatal("clip never loaded")
	}

	if err := c.SetChapters([]Chapter{{Title: "▶ IN", Time: 0.5}, {Title: "OUT ◀", Time: 1.5}}); err != nil {
		t.Fatalf("SetChapters: %v", err)
	}
	data, err := c.Command("get_property", "chapter-list")
	if err != nil {
		t.Fatalf("get chapter-list: %v", err)
	}
	var got []Chapter
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode chapter-list: %v", err)
	}
	if len(got) != 2 || got[0].Title != "▶ IN" || got[1].Title != "OUT ◀" {
		t.Fatalf("chapter-list = %+v, want IN/OUT at 0.5/1.5", got)
	}

	// A-B loop draws the IN/OUT region on the seekbar.
	if err := c.SetProp("ab-loop-a", 0.5); err != nil {
		t.Fatalf("set ab-loop-a: %v", err)
	}
	if err := c.SetProp("ab-loop-b", 1.5); err != nil {
		t.Fatalf("set ab-loop-b: %v", err)
	}
	abData, err := c.Command("get_property", "ab-loop-a")
	if err != nil {
		t.Fatalf("get ab-loop-a: %v", err)
	}
	var a float64
	if err := json.Unmarshal(abData, &a); err != nil || a != 0.5 {
		t.Fatalf("ab-loop-a = %s (%v), want 0.5", abData, err)
	}

	// The persistent HUD overlay command is accepted.
	if err := c.OSDOverlay(hudID, "{\\an7}movielily"); err != nil {
		t.Fatalf("osd-overlay: %v", err)
	}
}
