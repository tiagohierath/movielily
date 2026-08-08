package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"milklily/internal/project"
)

func TestQueueLastRenderStagesVideoAndTitle(t *testing.T) {
	root := t.TempDir()
	p := &project.Project{Root: root}
	source := filepath.Join(root, "exports", "video", "lesson.mp4")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("render bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "youtube_upload.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := filepath.Join(root, "youtube-queue")
	t.Setenv("MILKLILY_YOUTUBE", script)
	t.Setenv("MILKLILY_YOUTUBE_QUEUE", queue)

	target, err := QueueLastRender(p, source, "My lesson")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(target) != queue || !strings.HasSuffix(target, ".mp4") {
		t.Fatalf("target = %q", target)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "render bytes" {
		t.Fatalf("queued video = %q, err = %v", got, err)
	}
	sidecar := strings.TrimSuffix(target, ".mp4") + ".title.txt"
	if got, err := os.ReadFile(sidecar); err != nil || string(got) != "My lesson\n" {
		t.Fatalf("title sidecar = %q, err = %v", got, err)
	}

	// Re-queueing the same bytes is idempotent.
	again, err := QueueLastRender(p, source, "Different title")
	if err != nil || again != target {
		t.Fatalf("second queue = %q, %v; want %q, nil", again, err, target)
	}
}
