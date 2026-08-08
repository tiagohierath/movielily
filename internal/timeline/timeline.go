// Package timeline resolves a sequence into the structured plan that both the
// ffmpeg export and the mpv review are built from: the playable items in
// order with their absolute start times, the overlays bound to the scenes
// they ride, and the background beds. Centralising it keeps the two renderers
// from drifting apart on overlay windows, bed placement and timeline offsets
// (which they did, twice, before this existed).
package timeline

import (
	"fmt"
	"strconv"
	"strings"

	"milklily/internal/model"
	"milklily/internal/store"
)

// Scene is one playable item plus where it lands in the finished movie.
type Scene struct {
	Item  model.SequenceItem
	Start float64 // absolute seconds from the top of the cut
}

// End is the scene's exclusive end time on the timeline.
func (s Scene) End() float64 { return s.Start + s.Item.Duration() }

// Overlay is an image/card riding a scene, resolved to its absolute window.
type Overlay struct {
	Item       model.SequenceItem
	Start, End float64
}

// Plan is the resolved sequence: everything a renderer needs, no re-derivation.
type Plan struct {
	Scenes   []Scene
	Overlays []Overlay
	Beds     []model.SequenceItem
	Total    float64 // full runtime in seconds
}

// Resolve expands use|sequence splices, then walks the items once to build the
// plan. sequencesDir is where nested sequences are looked up. Overlays with no
// scene above them are an error; overlays that start past their scene's end
// are dropped (with the reason returned in warnings).
func Resolve(sequencesDir string, items []model.SequenceItem) (Plan, []string, error) {
	items, err := store.Expand(sequencesDir, items)
	if err != nil {
		return Plan{}, nil, err
	}
	var pl Plan
	var warnings []string
	offset, lastStart, lastDur := 0.0, 0.0, 0.0
	havePlayable := false
	for _, it := range items {
		switch {
		case it.IsSection():
		case it.IsAudio():
			pl.Beds = append(pl.Beds, it)
		case it.IsOverlay():
			if !havePlayable {
				return Plan{}, nil, fmt.Errorf("overlay %q has no scene above it to ride on", it.File)
			}
			s := lastStart + it.In
			e := s + it.Dur
			if it.Dur <= 0 || e > lastStart+lastDur {
				e = lastStart + lastDur // to the end of its scene
			}
			if s >= e {
				warnings = append(warnings, fmt.Sprintf("overlay %q starts after its scene ends, skipping", it.File))
				continue
			}
			pl.Overlays = append(pl.Overlays, Overlay{Item: it, Start: s, End: e})
		default:
			lastStart, lastDur, havePlayable = offset, it.Duration(), true
			pl.Scenes = append(pl.Scenes, Scene{Item: it, Start: offset})
			offset += it.Duration()
		}
	}
	pl.Total = offset
	if err := resolveBedAnchors(&pl); err != nil {
		return Plan{}, nil, err
	}
	return pl, warnings, nil
}

// resolveBedAnchors turns editorial anchors into the current absolute time.
// #at_image_N follows the Nth still image; #at_scene_N follows the Nth
// playable scene. An explicit #at_S remains the deliberate fixed-time choice.
func resolveBedAnchors(pl *Plan) error {
	for i := range pl.Beds {
		bed := &pl.Beds[i]
		if _, fixed := model.TagNumber(bed.Note, "at"); fixed {
			continue
		}
		if n, ok, err := positiveTagInt(bed.Note, "at_scene"); err != nil {
			return fmt.Errorf("audio bed %q: %w", bed.File, err)
		} else if ok {
			if n > len(pl.Scenes) {
				return fmt.Errorf("audio bed %q: #at_scene_%d is beyond the %d playable scenes", bed.File, n, len(pl.Scenes))
			}
			bed.Note = withAbsoluteAt(bed.Note, pl.Scenes[n-1].Start)
			continue
		}
		if n, ok, err := positiveTagInt(bed.Note, "at_image"); err != nil {
			return fmt.Errorf("audio bed %q: %w", bed.File, err)
		} else if ok {
			images := 0
			for _, scene := range pl.Scenes {
				if scene.Item.Kind != model.KindImage {
					continue
				}
				images++
				if images == n {
					bed.Note = withAbsoluteAt(bed.Note, scene.Start)
					break
				}
			}
			if images < n {
				return fmt.Errorf("audio bed %q: #at_image_%d is beyond the %d still images", bed.File, n, images)
			}
		}
	}
	return nil
}

func positiveTagInt(note, name string) (int, bool, error) {
	prefix := "#" + name + "_"
	for _, tag := range model.Tags(note) {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		n, err := strconv.Atoi(tag[len(prefix):])
		if err != nil || n < 1 {
			return 0, true, fmt.Errorf("%s must name a positive whole number", tag)
		}
		return n, true, nil
	}
	return 0, false, nil
}

func withAbsoluteAt(note string, at float64) string {
	return strings.TrimSpace(note + " #at_" + model.FormatSeconds(at))
}

// ResolveFrom is Resolve for the review's "start at scene N" case: it resolves
// the whole cut so overlay/offset math is correct, then reports which scene
// index corresponds to the caller's `from` (an index into the ORIGINAL items,
// pre-expansion, counting only playable items). Returns the plan plus the
// second-offset of that start scene.
func ResolveFrom(sequencesDir string, items []model.SequenceItem, fromScene int) (Plan, float64, error) {
	pl, _, err := Resolve(sequencesDir, items)
	if err != nil {
		return Plan{}, 0, err
	}
	if fromScene <= 0 || fromScene >= len(pl.Scenes) {
		return pl, 0, nil
	}
	return pl, pl.Scenes[fromScene].Start, nil
}
