package mpv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"movielily/internal/model"
	"movielily/internal/project"
)

// edlQuote uses mpv's length-prefixed quoting so paths with spaces, commas or
// unicode survive intact: %<bytelen>%<string>.
func edlQuote(s string) string { return fmt.Sprintf("%%%d%%%s", len(s), s) }

// BuildEDL writes an mpv EDL for the video items of a sequence and returns its
// path. Images can't be represented in an EDL; their count is returned so the
// caller can tell the user to export to see them.
func BuildEDL(p *project.Project, name string, items []model.SequenceItem) (path string, videos, skippedImages int, err error) {
	var b strings.Builder
	b.WriteString("# mpv EDL v0\n")
	for _, it := range items {
		if it.IsSection() {
			continue
		}
		if it.Kind != model.KindVideo {
			skippedImages++
			continue
		}
		abs, e := p.ResolveFootage(it.File)
		if e != nil {
			return "", 0, 0, e
		}
		fmt.Fprintf(&b, "%s,%s,%s\n", edlQuote(abs), model.FormatSeconds(it.In), model.FormatSeconds(it.Duration()))
		videos++
	}
	if videos == 0 {
		return "", 0, skippedImages, fmt.Errorf("sequence %q has no video items to review", name)
	}
	if err := os.MkdirAll(p.SequencesDir(), 0o755); err != nil {
		return "", 0, 0, err
	}
	path = filepath.Join(p.SequencesDir(), "."+name+".review.edl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", 0, 0, err
	}
	return path, videos, skippedImages, nil
}

// Review builds the EDL and plays the sequence in mpv without rendering.
func Review(p *project.Project, name string, items []model.SequenceItem) error {
	path, videos, skipped, err := BuildEDL(p, name, items)
	if err != nil {
		return err
	}
	fmt.Printf("reviewing %q — %d clip(s)", name, videos)
	if skipped > 0 {
		fmt.Printf(" (%d image(s) skipped — run 'movielily export' to see them)", skipped)
	}
	fmt.Println()

	cmd := exec.Command("mpv", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
