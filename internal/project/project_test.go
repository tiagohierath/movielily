package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesShortFilmWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "film")
	p, err := Init(root, "film")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		ConfigName,
		".gitignore",
		"README.md",
		"README.txt",
		"scripts/README.txt",
		"storyboards/inbox/README.txt",
		"storyboards/scenes",
		"images/stills",
		"refs/visual",
		"audio/dialogue",
		"audio/music",
		"fxs/overlays",
		"footage/raw",
		"sequences/README.txt",
		"exports/storyboard-books",
		"markers.txt",
		"notes.txt",
		"selects.txt",
		"sequences/main.txt",
	} {
		path := filepath.Join(p.Root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was not created: %v", rel, err)
		}
	}
}

func TestEnsureStructureDoesNotOverwriteUserFiles(t *testing.T) {
	p := testProject(t)
	readme := filepath.Join(p.Root, "README.md")
	seq := p.Sequence("main")
	if err := os.WriteFile(readme, []byte("my github notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seq, []byte("section|Custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureStructure(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(readme); string(got) != "my github notes\n" {
		t.Fatalf("README.md was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(seq); string(got) != "section|Custom\n" {
		t.Fatalf("main sequence was overwritten: %q", got)
	}
}

func TestEnsureGitignoreAppendsMilklilyBlockOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("local.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)
	if body[:10] != "local.tmp\n" {
		t.Fatalf("existing ignore content not preserved: %q", body)
	}
	if n := countOccurrences(body, "# >>> milklily"); n != 1 {
		t.Fatalf("milklily ignore block count = %d, want 1:\n%s", n, body)
	}
	for _, want := range []string{
		"/storyboards/**",
		"!/storyboards/inbox/README.txt",
		"/exports/**",
		"!/exports/storyboard-books/README.txt",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, body)
		}
	}
}

func TestResolveFootageAndStoreNameProjectFolders(t *testing.T) {
	p := testProject(t)
	story := touch(t, p.Root, "storyboards/scenes/opening/001.png")
	audio := touch(t, p.Root, "audio/dialogue/take.wav")
	oldFlat := touch(t, p.Root, "footage/old.mov")
	nestedFootage := touch(t, p.Root, "footage/raw/camera.mov")

	if got := mustResolve(t, p, "storyboards/scenes/opening/001.png"); got != story {
		t.Fatalf("ResolveFootage(storyboards path) = %q, want %q", got, story)
	}
	if got := mustResolve(t, p, "take.wav"); got != audio {
		t.Fatalf("ResolveFootage(unique basename) = %q, want %q", got, audio)
	}
	if got := p.StoreName(story); got != "storyboards/scenes/opening/001.png" {
		t.Fatalf("StoreName(story) = %q", got)
	}
	if got := p.StoreName(audio); got != "audio/dialogue/take.wav" {
		t.Fatalf("StoreName(audio) = %q", got)
	}
	if got := p.StoreName(oldFlat); got != "old.mov" {
		t.Fatalf("StoreName(old flat footage) = %q", got)
	}
	if got := p.StoreName(nestedFootage); got != "footage/raw/camera.mov" {
		t.Fatalf("StoreName(nested footage) = %q", got)
	}
}

func TestResolveFootageRejectsAmbiguousBasename(t *testing.T) {
	p := testProject(t)
	touch(t, p.Root, "storyboards/inbox/shot.png")
	touch(t, p.Root, "images/stills/shot.png")

	if _, err := p.ResolveFootage("shot.png"); err == nil {
		t.Fatal("ResolveFootage(ambiguous basename) succeeded; want error")
	}
}

func testProject(t *testing.T) *Project {
	t.Helper()
	root := t.TempDir()
	p, err := Init(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func touch(t *testing.T, root, rel string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustResolve(t *testing.T, p *Project, name string) string {
	t.Helper()
	got, err := p.ResolveFootage(name)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func countOccurrences(s, sub string) int {
	n := 0
	for {
		i := strings.Index(s, sub)
		if i < 0 {
			return n
		}
		n++
		s = s[i+len(sub):]
	}
}
