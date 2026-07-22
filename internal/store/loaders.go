package store

import (
	"fmt"
	"path/filepath"
	"strings"

	"movielily/internal/model"
)

func LoadMarkers(path string) ([]model.Marker, error) {
	lines, err := RawLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]model.Marker, 0, len(lines))
	for _, l := range lines {
		m, err := model.ParseMarker(l)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func LoadSelects(path string) ([]model.Select, error) {
	lines, err := RawLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]model.Select, 0, len(lines))
	for _, l := range lines {
		s, err := model.ParseSelect(l)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func LoadNotes(path string) ([]model.Note, error) {
	lines, err := RawLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]model.Note, 0, len(lines))
	for _, l := range lines {
		n, err := model.ParseNote(l)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func LoadSequence(path string) ([]model.SequenceItem, error) {
	lines, err := RawLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]model.SequenceItem, 0, len(lines))
	for _, l := range lines {
		it, err := model.ParseItem(l)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, it)
	}
	return out, nil
}

// Expand splices `use|sequence` items in place: each is replaced by the
// referenced sequence's items (recursively), so long films assemble from
// per-chapter sequences. dir is the sequences folder; cycles and missing
// files fail loudly.
func Expand(dir string, items []model.SequenceItem) ([]model.SequenceItem, error) {
	return expand(dir, items, map[string]bool{}, 0)
}

func expand(dir string, items []model.SequenceItem, seen map[string]bool, depth int) ([]model.SequenceItem, error) {
	if depth > 10 {
		return nil, fmt.Errorf("sequences nested more than 10 deep (a use loop?)")
	}
	out := make([]model.SequenceItem, 0, len(items))
	for _, it := range items {
		if it.Kind != model.KindUse {
			out = append(out, it)
			continue
		}
		name := strings.TrimSuffix(it.File, ".txt")
		if seen[name] {
			return nil, fmt.Errorf("sequence %q uses itself (directly or via a loop)", name)
		}
		sub, err := LoadSequence(filepath.Join(dir, name+".txt"))
		if err != nil {
			return nil, fmt.Errorf("use|%s: %w", name, err)
		}
		seen[name] = true
		subExp, err := expand(dir, sub, seen, depth+1)
		delete(seen, name)
		if err != nil {
			return nil, err
		}
		out = append(out, subExp...)
	}
	return out, nil
}
