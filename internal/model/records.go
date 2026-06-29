package model

import (
	"fmt"
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
	KindVideo ItemKind = "video"
	KindImage ItemKind = "image"
)

// SequenceItem is one entry in a sequence: a trimmed video clip or a still
// image shown for a duration.
//
//	video|file|in|out|note      e.g. video|clip001.mp4|72.3|85.1|great reaction
//	image|file|duration|note    e.g. image|photo001.jpg|5|opening image
type SequenceItem struct {
	Kind ItemKind
	File string
	In   float64 // video only
	Out  float64 // video only
	Dur  float64 // image only
	Note string
}

// Duration is how long the item occupies the finished sequence.
func (it SequenceItem) Duration() float64 {
	if it.Kind == KindImage {
		return it.Dur
	}
	return it.Out - it.In
}

func (it SequenceItem) String() string {
	if it.Kind == KindImage {
		return "image|" + field(it.File) + "|" + FormatSeconds(it.Dur) + "|" + text(it.Note)
	}
	return "video|" + field(it.File) + "|" + FormatSeconds(it.In) + "|" + FormatSeconds(it.Out) + "|" + text(it.Note)
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
	default:
		return SequenceItem{}, fmt.Errorf("unknown item kind in %q (want video|… or image|…)", line)
	}
}
