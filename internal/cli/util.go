package cli

import (
	"fmt"
	"strings"

	"movielily/internal/model"
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
