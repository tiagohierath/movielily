package model

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Marker is a single point in time worth revisiting.
//
//	file|seconds|note   e.g. clip001.mp4|72.3|funny reaction
type Marker struct {
	File string
	Time float64
	Note string
}

func (m Marker) String() string {
	return field(m.File) + "|" + FormatSeconds(m.Time) + "|" + text(m.Note)
}

func ParseMarker(line string) (Marker, error) {
	p := strings.SplitN(line, "|", 3)
	if len(p) < 2 {
		return Marker{}, fmt.Errorf("invalid marker %q (want file|seconds|note)", line)
	}
	t, err := ParseSeconds(p[1])
	if err != nil {
		return Marker{}, err
	}
	m := Marker{File: strings.TrimSpace(p[0]), Time: t}
	if len(p) == 3 {
		m.Note = strings.TrimSpace(p[2])
	}
	return m, nil
}

// Select is a clip with IN and OUT points — a moment worth keeping.
//
//	file|in|out|note    e.g. clip001.mp4|72.3|85.1|great reaction
type Select struct {
	File string
	In   float64
	Out  float64
	Note string
}

func (s Select) String() string {
	return field(s.File) + "|" + FormatSeconds(s.In) + "|" + FormatSeconds(s.Out) + "|" + text(s.Note)
}

func (s Select) Duration() float64 { return s.Out - s.In }

// AsItem promotes a select into a sequence item.
func (s Select) AsItem() SequenceItem {
	return SequenceItem{Kind: KindVideo, File: s.File, In: s.In, Out: s.Out, Note: s.Note}
}

func ParseSelect(line string) (Select, error) {
	p := strings.SplitN(line, "|", 4)
	if len(p) < 3 {
		return Select{}, fmt.Errorf("invalid select %q (want file|in|out|note)", line)
	}
	in, err := ParseSeconds(p[1])
	if err != nil {
		return Select{}, err
	}
	out, err := ParseSeconds(p[2])
	if err != nil {
		return Select{}, err
	}
	s := Select{File: strings.TrimSpace(p[0]), In: in, Out: out}
	if len(p) == 4 {
		s.Note = strings.TrimSpace(p[3])
	}
	return s, nil
}

// Note is an observation, optionally anchored to a clip and time.
//
//	file|time|text   (file and/or time may be empty)
type Note struct {
	File    string
	Time    float64
	HasTime bool
	Text    string
}

func (n Note) String() string {
	ts := ""
	if n.HasTime {
		ts = FormatSeconds(n.Time)
	}
	return field(n.File) + "|" + ts + "|" + text(n.Text)
}

func ParseNote(line string) (Note, error) {
	p := strings.SplitN(line, "|", 3)
	switch len(p) {
	case 1:
		return Note{Text: strings.TrimSpace(p[0])}, nil
	case 2:
		return Note{File: strings.TrimSpace(p[0]), Text: strings.TrimSpace(p[1])}, nil
	default:
		n := Note{File: strings.TrimSpace(p[0]), Text: strings.TrimSpace(p[2])}
		if ts := strings.TrimSpace(p[1]); ts != "" {
			t, err := ParseSeconds(ts)
			if err != nil {
				return Note{}, err
			}
			n.Time, n.HasTime = t, true
		}
		return n, nil
	}
}

// ItemKind is the type of a sequence entry.
type ItemKind string

const (
	KindVideo   ItemKind = "video"
	KindImage   ItemKind = "image"
	KindSection ItemKind = "section"
	KindAudio   ItemKind = "audio"
	KindTitle   ItemKind = "title"
	KindOverlay ItemKind = "overlay"
	KindAnim    ItemKind = "anim"
)

// IsAudioFile reports whether a media reference is a pure audio file (a voice
// recording, music). Such files can sit in the timeline as `video|` items: the
// sound occupies the slot and the picture is a black canvas to decorate with
// overlays and cards. That's the narration-first workflow: record the voice
// anywhere, drop it in footage/, select on it, build the film on top.
func IsAudioFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wav", ".mp3", ".m4a", ".flac", ".ogg", ".aac", ".opus":
		return true
	}
	return false
}

// SequenceItem is one entry in a sequence: a trimmed video clip, a still image
// shown for a duration, or a section header that groups the items beneath it
// (a "folder" — e.g. "Scene 1"). A section contributes nothing to the rendered
// movie; it is purely organisational.
//
// An audio item is a background bed (music or narration) laid UNDER the whole
// sequence on export: it starts at 0, is mixed below the clips' own sound at
// its gain (dB, usually negative for music), and is cut when the video ends.
// It occupies no slot in the timeline.
//
// A title item is a generated title card: a typst template from the project's
// titles/ folder rendered with this item's text, shown full-frame for the
// duration, exactly like an image. One template, many cards: only the text
// changes per use.
//
// An overlay item decorates the scene DIRECTLY ABOVE it in the sequence: the
// image appears on top of that scene's picture `at` seconds into it, for
// `dur` seconds (clamped to the scene), at `place`. It occupies no timeline
// slot, and moving the scene moves its overlays with it (they travel as
// neighbouring lines). Place is corner:percent, e.g. br:33 = bottom-right at
// 33% of the frame width; corners tl/tr/bl/br, c = center, full = full-frame.
//
//	video|file|in|out|note        e.g. video|clip001.mp4|72.3|85.1|great reaction
//	image|file|duration|note      e.g. image|photo001.jpg|5|opening image
//	audio|file|gain|note          e.g. audio|song.mp3|-12|music bed
//	title|template|dur|text       e.g. title|chapter.typ|4|Capítulo 1
//	overlay|file|at|dur|place|note  e.g. overlay|ref.png|2|5|tr:30|the reference
//	section|title                 e.g. section|Scene 1, the arrival
type SequenceItem struct {
	Kind  ItemKind
	File  string
	In    float64 // video: in point · overlay: seconds into its scene
	Out   float64 // video only
	Dur   float64 // image, title and overlay
	Gain  float64 // audio only, dB relative to the source (0 = as recorded)
	Place string  // overlay only: corner[:percent] or "full"
	Note  string  // free text; a section's heading; a title card's TEXT
}

// IsSection reports whether the item is a section header rather than playable
// footage.
func (it SequenceItem) IsSection() bool { return it.Kind == KindSection }

// IsAudio reports whether the item is a background audio bed rather than a
// slot in the timeline.
func (it SequenceItem) IsAudio() bool { return it.Kind == KindAudio }

// IsOverlay reports whether the item rides on top of another scene rather
// than occupying the timeline itself.
func (it SequenceItem) IsOverlay() bool { return it.Kind == KindOverlay }

// Duration is how long the item occupies the finished sequence. Sections
// occupy no time; audio beds and overlays ride along without extending it.
func (it SequenceItem) Duration() float64 {
	switch it.Kind {
	case KindSection, KindAudio, KindOverlay:
		return 0
	case KindImage, KindTitle, KindAnim:
		return it.Dur
	}
	return it.Out - it.In
}

func (it SequenceItem) String() string {
	switch it.Kind {
	case KindSection:
		return "section|" + text(it.Note)
	case KindImage:
		return "image|" + field(it.File) + "|" + FormatSeconds(it.Dur) + "|" + text(it.Note)
	case KindAudio:
		return "audio|" + field(it.File) + "|" + FormatSeconds(it.Gain) + "|" + text(it.Note)
	case KindTitle:
		return "title|" + field(it.File) + "|" + FormatSeconds(it.Dur) + "|" + text(it.Note)
	case KindAnim:
		return "anim|" + field(it.File) + "|" + FormatSeconds(it.Dur) + "|" + text(it.Note)
	case KindOverlay:
		place := it.Place
		if place == "" {
			place = DefaultPlace
		}
		return "overlay|" + field(it.File) + "|" + FormatSeconds(it.In) + "|" + FormatSeconds(it.Dur) + "|" + place + "|" + text(it.Note)
	}
	return "video|" + field(it.File) + "|" + FormatSeconds(it.In) + "|" + FormatSeconds(it.Out) + "|" + text(it.Note)
}

// DefaultPlace is where an overlay sits when no place is given: bottom-right,
// a third of the frame wide.
const DefaultPlace = "br:33"

var placeRe = regexp.MustCompile(`^(tl|tr|bl|br|c)(:[0-9]{1,3})?$|^full$`)

// ParsePlace validates an overlay place spec and splits it into its corner
// keyword and width percent (of the frame). "full" returns ("full", 100).
func ParsePlace(s string) (corner string, pct int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultPlace
	}
	if !placeRe.MatchString(s) {
		return "", 0, fmt.Errorf("invalid place %q (want tl/tr/bl/br/c[:percent] or full, e.g. br:33)", s)
	}
	if s == "full" {
		return "full", 100, nil
	}
	corner = s
	pct = 33
	if c, p, ok := strings.Cut(s, ":"); ok {
		corner = c
		if pct, err = strconv.Atoi(p); err != nil || pct < 1 || pct > 100 {
			return "", 0, fmt.Errorf("invalid place %q (percent must be 1..100)", s)
		}
	}
	return corner, pct, nil
}

func ParseItem(line string) (SequenceItem, error) {
	kind := line
	if i := strings.IndexByte(line, '|'); i >= 0 {
		kind = line[:i]
	}
	switch ItemKind(strings.TrimSpace(kind)) {
	case KindVideo:
		p := strings.SplitN(line, "|", 5)
		if len(p) < 4 {
			return SequenceItem{}, fmt.Errorf("invalid video item %q (want video|file|in|out|note)", line)
		}
		in, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, err
		}
		out, err := ParseSeconds(p[3])
		if err != nil {
			return SequenceItem{}, err
		}
		it := SequenceItem{Kind: KindVideo, File: strings.TrimSpace(p[1]), In: in, Out: out}
		if len(p) == 5 {
			it.Note = strings.TrimSpace(p[4])
		}
		return it, nil
	case KindImage:
		p := strings.SplitN(line, "|", 4)
		if len(p) < 3 {
			return SequenceItem{}, fmt.Errorf("invalid image item %q (want image|file|duration|note)", line)
		}
		dur, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, err
		}
		it := SequenceItem{Kind: KindImage, File: strings.TrimSpace(p[1]), Dur: dur}
		if len(p) == 4 {
			it.Note = strings.TrimSpace(p[3])
		}
		return it, nil
	case KindTitle:
		p := strings.SplitN(line, "|", 4)
		if len(p) < 4 {
			return SequenceItem{}, fmt.Errorf("invalid title item %q (want title|template|dur|text)", line)
		}
		dur, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, err
		}
		return SequenceItem{Kind: KindTitle, File: strings.TrimSpace(p[1]), Dur: dur,
			Note: strings.TrimSpace(p[3])}, nil
	case KindAnim:
		p := strings.SplitN(line, "|", 4)
		if len(p) < 4 {
			return SequenceItem{}, fmt.Errorf("invalid anim item %q (want anim|template|dur|text)", line)
		}
		dur, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, err
		}
		return SequenceItem{Kind: KindAnim, File: strings.TrimSpace(p[1]), Dur: dur,
			Note: strings.TrimSpace(p[3])}, nil
	case KindOverlay:
		p := strings.SplitN(line, "|", 6)
		if len(p) < 5 {
			return SequenceItem{}, fmt.Errorf("invalid overlay %q (want overlay|file|at|dur|place|note)", line)
		}
		at, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, err
		}
		dur, err := ParseSeconds(p[3])
		if err != nil {
			return SequenceItem{}, err
		}
		place := strings.TrimSpace(p[4])
		if _, _, err := ParsePlace(place); err != nil {
			return SequenceItem{}, err
		}
		it := SequenceItem{Kind: KindOverlay, File: strings.TrimSpace(p[1]), In: at, Dur: dur, Place: place}
		if len(p) == 6 {
			it.Note = strings.TrimSpace(p[5])
		}
		return it, nil
	case KindAudio:
		p := strings.SplitN(line, "|", 4)
		if len(p) < 3 {
			return SequenceItem{}, fmt.Errorf("invalid audio item %q (want audio|file|gain|note)", line)
		}
		gain, err := ParseSeconds(p[2])
		if err != nil {
			return SequenceItem{}, fmt.Errorf("invalid gain in %q (want dB, e.g. -12)", line)
		}
		it := SequenceItem{Kind: KindAudio, File: strings.TrimSpace(p[1]), Gain: gain}
		if len(p) == 4 {
			it.Note = strings.TrimSpace(p[3])
		}
		return it, nil
	case KindSection:
		p := strings.SplitN(line, "|", 2)
		it := SequenceItem{Kind: KindSection}
		if len(p) == 2 {
			it.Note = strings.TrimSpace(p[1])
		}
		return it, nil
	default:
		return SequenceItem{}, fmt.Errorf("unknown item kind in %q (want video|…, image|… or section|…)", line)
	}
}
