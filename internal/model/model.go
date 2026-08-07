// Package model defines movielily's plain-text records and how they are parsed
// and formatted. It performs no I/O so it is easy to test, and the formats here
// are the project's real on-disk contract.
package model

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FormatSeconds renders seconds as compactly as possible while round-tripping
// exactly: 72.3 -> "72.3", 5.0 -> "5", 85.1 -> "85.1". Timestamps are always
// stored as seconds; frame numbers are never used.
func FormatSeconds(s float64) string {
	return strconv.FormatFloat(s, 'f', -1, 64)
}

// ParseSeconds accepts plain seconds ("72.3", "5"), an optional trailing "s",
// or clock notation ("1:12.5", "1:02:03"). It always yields seconds.
func ParseSeconds(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty timestamp")
	}
	s = strings.TrimSuffix(s, "s")
	if strings.Contains(s, ":") {
		return parseClock(s)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp %q", s)
	}
	return v, nil
}

func parseClock(s string) (float64, error) {
	var total float64
	for _, p := range strings.Split(s, ":") {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid timestamp %q", s)
		}
		total = total*60 + v
	}
	return total, nil
}

var tagRe = regexp.MustCompile(`#[\p{L}\p{N}_\-+.]+`)
var imagePanTagRe = regexp.MustCompile(`(?i)#(?:pan_(?:lr|rl|tb|bt)|ease_(?:linear|in|out|inout))\b`)

// Tags extracts #hashtags from free text, lower-cased and de-duplicated,
// preserving first-seen order. Tags are how footage is labelled (#best #funny).
func Tags(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tagRe.FindAllString(text, -1) {
		t = strings.ToLower(t)
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

type PanDirection string

const (
	PanLeftRight PanDirection = "lr"
	PanRightLeft PanDirection = "rl"
	PanTopBottom PanDirection = "tb"
	PanBottomTop PanDirection = "bt"
)

type PanEase string

const (
	PanEaseLinear PanEase = "linear"
	PanEaseIn     PanEase = "in"
	PanEaseOut    PanEase = "out"
	PanEaseInOut  PanEase = "inout"
)

type PanSpec struct {
	Direction PanDirection
	Ease      PanEase
}

// ImagePan reads simple orthogonal pan motion from note tags. The pan tags are
// deliberately plain text so browser edits, TUI edits and git diffs all share
// the same representation:
//
//	#pan_lr #pan_rl #pan_tb #pan_bt
//	#ease_linear #ease_in #ease_out #ease_inout
func ImagePan(note string) (PanSpec, bool) {
	spec := PanSpec{Ease: PanEaseLinear}
	for _, t := range Tags(note) {
		switch t {
		case "#pan_lr":
			spec.Direction = PanLeftRight
		case "#pan_rl":
			spec.Direction = PanRightLeft
		case "#pan_tb":
			spec.Direction = PanTopBottom
		case "#pan_bt":
			spec.Direction = PanBottomTop
		case "#ease_linear":
			spec.Ease = PanEaseLinear
		case "#ease_in":
			spec.Ease = PanEaseIn
		case "#ease_out":
			spec.Ease = PanEaseOut
		case "#ease_inout":
			spec.Ease = PanEaseInOut
		}
	}
	if spec.Direction == "" {
		return PanSpec{}, false
	}
	return spec, true
}

func StripImagePan(note string) string {
	return strings.Join(strings.Fields(imagePanTagRe.ReplaceAllString(note, "")), " ")
}

func SetImagePan(note string, spec PanSpec) string {
	base := StripImagePan(note)
	if spec.Direction == "" {
		return base
	}
	if spec.Ease == "" {
		spec.Ease = PanEaseLinear
	}
	tags := "#pan_" + string(spec.Direction) + " #ease_" + string(spec.Ease)
	if base == "" {
		return tags
	}
	return base + " " + tags
}

func (p PanSpec) DirectionLabel() string {
	switch p.Direction {
	case PanLeftRight:
		return "L->R"
	case PanRightLeft:
		return "R->L"
	case PanTopBottom:
		return "T->B"
	case PanBottomTop:
		return "B->T"
	default:
		return ""
	}
}

func (p PanSpec) EaseLabel() string {
	switch p.Ease {
	case PanEaseIn:
		return "ease in"
	case PanEaseOut:
		return "ease out"
	case PanEaseInOut:
		return "ease in/out"
	default:
		return "linear"
	}
}

func (p PanSpec) Label() string {
	dir := p.DirectionLabel()
	if dir == "" {
		return ""
	}
	return "pan " + dir + " " + p.EaseLabel()
}

// TagCount pairs a tag with how often it occurs.
type TagCount struct {
	Tag   string
	Count int
}

// AllTags aggregates tags across many texts, sorted by count then name.
func AllTags(texts []string) []TagCount {
	counts := map[string]int{}
	for _, t := range texts {
		for _, tag := range Tags(t) {
			counts[tag]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for tag, n := range counts {
		out = append(out, TagCount{Tag: tag, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// field cleans a value used in a non-final, pipe-delimited position: pipes
// would break the format and newlines the one-record-per-line rule.
func field(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// text cleans the final free-text field. Pipes are allowed (the parser keeps
// the remainder of the line intact); newlines are not.
func text(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
