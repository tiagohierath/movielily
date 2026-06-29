package store

import (
	"fmt"

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
