// Package manim renders animated cards: small manim scenes that live in the
// project's anims/ folder and receive their text per use, mirroring the typst
// title cards but animated. movielily always renders them at the project's
// own frame and fps (4:3, 30fps by default), so templates carry only style.
// Rendered clips land in a content-addressed cache; a given template+text is
// rendered once and reused forever.
package manim

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"movielily/internal/project"
)

// DefaultTemplate is written to anims/card.py on first use. The contract:
// a template defines a Scene subclass named Card and reads its text from
// $MOVIELILY_TEXT. Frame size and fps come from movielily, not the file.
const DefaultTemplate = `# movielily animated card. The card's text arrives in $MOVIELILY_TEXT; this
# file is the reusable STYLE (duplicate it to make new ones). movielily
# renders it at the project's frame and fps (4:3 1440x1080, 30fps by
# default), so nothing here hard-codes a resolution.
#
# Contract: define a Scene subclass named Card.
import os

from manim import *


class Card(Scene):
    def construct(self):
        self.camera.background_color = "#000000"
        text = Text(os.environ.get("MOVIELILY_TEXT", "Title"), font_size=72)
        self.play(Write(text), run_time=1.5)
        self.wait(1.5)
        self.play(FadeOut(text), run_time=0.8)
`

// AnimsDir is where a project keeps its animated card templates.
func AnimsDir(p *project.Project) string { return filepath.Join(p.Root, "anims") }

func cacheDir(p *project.Project) string { return filepath.Join(p.Root, ".cache", "anims") }

// EnsureDefault makes sure anims/ exists with at least one template, creating
// card.py on first use. Returns the created name ("" if any already existed).
func EnsureDefault(p *project.Project) (created string, err error) {
	dir := AnimsDir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if ts, _ := Templates(p); len(ts) > 0 {
		return "", nil
	}
	if err := os.WriteFile(filepath.Join(dir, "card.py"), []byte(DefaultTemplate), 0o644); err != nil {
		return "", err
	}
	return "card.py", nil
}

// Templates lists the .py files in anims/ by base name.
func Templates(p *project.Project) ([]string, error) {
	entries, err := os.ReadDir(AnimsDir(p))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Resolve turns a template reference into an absolute path, checking anims/
// first (with or without .py), then the path as given.
func Resolve(p *project.Project, name string) (string, error) {
	candidates := []string{
		filepath.Join(AnimsDir(p), name),
		filepath.Join(AnimsDir(p), name+".py"),
		name,
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("anim template not found: %q (looked in %s)", name, AnimsDir(p))
}

// StoreName is how a template is recorded in a sequence: its base name.
func StoreName(name string) string { return filepath.Base(name) }

func cachePath(p *project.Project, tplSrc []byte, text string) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%dx%d@%d", tplSrc, text, p.Config.Width, p.Config.Height, p.Config.FPS)
	return filepath.Join(cacheDir(p), fmt.Sprintf("%x.mp4", sum.Sum(nil)[:12]))
}

// Cached reports whether this template+text is already rendered, without
// rendering anything (the TUI preview uses this: a manim render is far too
// heavy to trigger from a cursor movement).
func Cached(p *project.Project, template, text string) (string, bool) {
	tpl, err := Resolve(p, template)
	if err != nil {
		return "", false
	}
	src, err := os.ReadFile(tpl)
	if err != nil {
		return "", false
	}
	out := cachePath(p, src, text)
	_, err = os.Stat(out)
	return out, err == nil
}

// Render compiles the template with the text into a cached mp4 at the
// project's frame and fps, and returns its path. Slow the first time for a
// given template+text (it runs manim); instant afterwards.
func Render(p *project.Project, template, text string) (string, error) {
	tpl, err := Resolve(p, template)
	if err != nil {
		return "", err
	}
	src, err := os.ReadFile(tpl)
	if err != nil {
		return "", err
	}
	out := cachePath(p, src, text)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if err := os.MkdirAll(cacheDir(p), 0o755); err != nil {
		return "", err
	}

	argv, err := manimCommand()
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp("", "movielily-manim-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	args := append(argv[1:], "render",
		"--resolution", fmt.Sprintf("%d,%d", p.Config.Width, p.Config.Height),
		"--fps", strconv.Itoa(p.Config.FPS),
		"--media_dir", work,
		"--output_file", "card.mp4",
		tpl, "Card",
	)
	cmd := exec.Command(argv[0], args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr // renders take a while; show progress
	cmd.Env = append(envWithoutNixBreakage(argv[0]), "MOVIELILY_TEXT="+text)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("manim render failed for %s (is the scene named Card?): %w", StoreName(template), err)
	}

	// manim nests its output under media_dir/videos/<file>/<quality>/; take
	// the card we asked for wherever it landed.
	matches, _ := filepath.Glob(filepath.Join(work, "videos", "*", "*", "card.mp4"))
	if len(matches) == 0 {
		return "", fmt.Errorf("manim finished but produced no card.mp4 under %s", work)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	return out, os.Rename(tmp, out)
}

// Probe returns a media file's duration in seconds via ffprobe.
func Probe(path string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe gave no duration for %s", path)
	}
	return d, nil
}

// manimCommand finds manim: on PATH, or via an ephemeral nix shell.
func manimCommand() ([]string, error) {
	if _, err := exec.LookPath("manim"); err == nil {
		return []string{"manim"}, nil
	}
	if _, err := exec.LookPath("nix"); err == nil {
		return []string{"nix", "shell", "nixpkgs#manim", "--command", "manim"}, nil
	}
	return nil, fmt.Errorf("manim not found on PATH (animated cards need manim; everything else works without it)")
}

// envWithoutNixBreakage strips LD_LIBRARY_PATH when running through nix (a
// leaked host value breaks nix-provided binaries against their glibc).
func envWithoutNixBreakage(argv0 string) []string {
	env := os.Environ()
	if argv0 != "nix" {
		return env
	}
	kept := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			kept = append(kept, kv)
		}
	}
	return kept
}
