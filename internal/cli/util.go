package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"movielily/internal/ffmpeg"
	"movielily/internal/model"
	"movielily/internal/project"
)

func joinArgs(args []string) string { return strings.TrimSpace(strings.Join(args, " ")) }

// matches reports whether term appears (case-insensitively) in any field.
func matches(term string, fields ...string) bool {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), term) {
			return true
		}
	}
	return false
}

func hasTag(name, text string) bool {
	for _, t := range model.Tags(text) {
		if t == name {
			return true
		}
	}
	return false
}

func printSection(title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Printf("%s:\n", title)
	for _, l := range lines {
		fmt.Printf("  %s\n", l)
	}
}

func formatNote(n model.Note) string {
	loc := n.File
	if n.HasTime {
		if loc != "" {
			loc += " "
		}
		loc += model.FormatSeconds(n.Time) + "s"
	}
	if loc != "" {
		return fmt.Sprintf("%-20s %s", loc, n.Text)
	}
	return n.Text
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

func secondsToFrames(seconds float64, fps int) int {
	if fps <= 0 {
		fps = 30
	}
	if seconds <= 0 {
		return 0
	}
	return int(seconds*float64(fps) + 0.5)
}

func storeExistingMedia(p *project.Project, name string) (string, error) {
	abs, err := p.ResolveFootage(name)
	if err != nil {
		return "", err
	}
	return p.StoreName(abs), nil
}

// refuseInsideFootage guards writes: nothing movielily produces may land in a
// source media folder.
func refuseInsideFootage(p *project.Project, out string) error {
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	for _, dir := range p.MediaDirs() {
		dirAbs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dirAbs, outAbs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			name, ok := p.RelPath(dirAbs)
			if !ok {
				name = filepath.Base(dirAbs)
			}
			return fmt.Errorf("refusing to write into %s/ (source media is read-only)", name)
		}
	}
	return nil
}

// frameGrab is the CLI shim over ffmpeg.Frame.
func frameGrab(src string, at float64, out string) error { return ffmpeg.Frame(src, at, out) }
