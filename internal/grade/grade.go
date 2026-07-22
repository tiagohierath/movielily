// Package grade is movielily's color-grading and film-grain engine. A grade is
// plain text: friendly key=value parameters (saturation=120, grain=30) with a
// neutral default for each, so it is fully reversible (delete the text),
// reproducible (same text, same pixels), and diffable. Grades never touch
// footage; they compile to one ffmpeg filter chain applied at export.
//
// Two ways to grade an item, both text in the sequence file:
//   - inline in a note:  clip note "sunset  saturation=120 grain=25"
//   - a named preset:    tag the note "#grade:filmic", defined in grades/*.grade
//
// Inline params override the preset, so a preset is a starting look you tweak
// per shot. The user speaks in friendly 0..200 / -100..100 numbers; the ugly
// ffmpeg values are generated, never typed (the "don't expose ffmpeg" rule).
package grade

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"movielily/internal/model"
)

// param describes one knob: its neutral value and the friendly range, plus how
// it maps into ffmpeg. order fixes the display and filter order.
type param struct {
	name    string
	order   int
	neutral float64
	min     float64
	max     float64
	help    string
}

// The canonical parameters. Neutral = "do nothing".
var params = map[string]param{
	"brightness": {"brightness", 0, 0, -100, 100, "-100 dark … 0 … 100 bright"},
	"contrast":   {"contrast", 1, 100, 0, 200, "0 flat … 100 normal … 200 punchy"},
	"saturation": {"saturation", 2, 100, 0, 200, "0 grey … 100 normal … 200 vivid"},
	"gamma":      {"gamma", 3, 100, 1, 300, "100 normal (lower = darker mids)"},
	"warmth":     {"warmth", 4, 0, -100, 100, "-100 cool/blue … 0 … 100 warm/orange"},
	"sharpen":    {"sharpen", 5, 0, 0, 100, "0 none … 100 crisp"},
	"grain":      {"grain", 6, 0, 0, 100, "0 clean … 100 heavy film grain"},
}

// aliases map friendly short/other names onto canonical ones.
var aliases = map[string]string{
	"bri": "brightness", "con": "contrast", "contrasts": "contrast",
	"sat": "saturation", "gam": "gamma",
	"temp": "warmth", "temperature": "warmth", "warm": "warmth",
	"sharp": "sharpen", "sharpness": "sharpen",
	"noise": "grain", "grains": "grain",
}

func canonical(k string) (string, bool) {
	k = strings.ToLower(strings.TrimSpace(k))
	if _, ok := params[k]; ok {
		return k, true
	}
	if c, ok := aliases[k]; ok {
		return c, true
	}
	return "", false
}

// Grade is a set of non-neutral parameter values (canonical keys).
type Grade struct {
	vals map[string]float64
}

func New() *Grade { return &Grade{vals: map[string]float64{}} }

// IsNeutral reports whether the grade changes nothing.
func (g *Grade) IsNeutral() bool { return g == nil || len(g.vals) == 0 }

// Set assigns a parameter (canonical or alias), dropping it if it equals the
// neutral value so the text stays minimal. Unknown keys and out-of-range
// values error, naming what is allowed.
func (g *Grade) Set(key string, v float64) error {
	c, ok := canonical(key)
	if !ok {
		return fmt.Errorf("unknown grade parameter %q (have: %s)", key, strings.Join(Names(), ", "))
	}
	p := params[c]
	if v < p.min || v > p.max {
		return fmt.Errorf("%s=%s out of range [%s..%s]", c, num(v), num(p.min), num(p.max))
	}
	if v == p.neutral {
		delete(g.vals, c)
	} else {
		g.vals[c] = v
	}
	return nil
}

// Names lists the canonical parameter names in display order.
func Names() []string {
	ns := make([]string, 0, len(params))
	for n := range params {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return params[ns[i]].order < params[ns[j]].order })
	return ns
}

// Spec is a parameter's display metadata, for the TUI grade panel.
type Spec struct {
	Name    string
	Neutral float64
	Min     float64
	Max     float64
	Help    string
}

// Specs returns every parameter in display order.
func Specs() []Spec {
	var out []Spec
	for _, n := range Names() {
		p := params[n]
		out = append(out, Spec{p.name, p.neutral, p.min, p.max, p.help})
	}
	return out
}

// Get returns the parameter's current value, or its neutral if unset.
func (g *Grade) Get(name string) float64 {
	c, ok := canonical(name)
	if !ok {
		return 0
	}
	if v, ok := g.vals[c]; ok {
		return v
	}
	return params[c].neutral
}

// Help returns "name  range" lines for every parameter, for `grade` output.
func Help() []string {
	var out []string
	for _, n := range Names() {
		out = append(out, fmt.Sprintf("  %-11s %s", n, params[n].help))
	}
	return out
}

// merge overlays other's set values on top of g (other wins). Used to apply
// inline note params over a named preset.
func (g *Grade) merge(other *Grade) {
	if other == nil {
		return
	}
	for k, v := range other.vals {
		g.vals[k] = v
	}
}

// String renders the grade as canonical "key=value" tokens in order, the exact
// form stored in notes and preset files.
func (g *Grade) String() string {
	if g.IsNeutral() {
		return ""
	}
	var toks []string
	for _, n := range Names() {
		if v, ok := g.vals[n]; ok {
			toks = append(toks, n+"="+num(v))
		}
	}
	return strings.Join(toks, " ")
}

// tokenRe matches a key=value grade token (letters = signed number).
var tokenRe = regexp.MustCompile(`^([a-zA-Z]+)=(-?[0-9]+(?:\.[0-9]+)?)$`)

// SplitNote separates a note into its human text and its grade. Grade tokens
// (key=value where key is a known parameter) are pulled out; everything else,
// including #tags, stays as the human note.
func SplitNote(note string) (human string, g *Grade) {
	g = New()
	var rest []string
	for _, tok := range strings.Fields(note) {
		if m := tokenRe.FindStringSubmatch(tok); m != nil {
			if _, ok := canonical(m[1]); ok {
				v, _ := strconv.ParseFloat(m[2], 64)
				_ = g.Set(m[1], v) // ignore range errors here; keep the token out of human text
				continue
			}
		}
		rest = append(rest, tok)
	}
	return strings.Join(rest, " "), g
}

// MergeIntoNote replaces the grade tokens in a note with g's, preserving the
// human text (and its #tags). Empty grade removes the tokens entirely.
func MergeIntoNote(note string, g *Grade) string {
	human, _ := SplitNote(note)
	tokens := g.String()
	switch {
	case human == "":
		return tokens
	case tokens == "":
		return human
	default:
		return human + " " + tokens
	}
}

// presetRef pulls the "#grade:name" tag from a note, if present.
var presetRe = regexp.MustCompile(`#grade:([a-zA-Z0-9_\-]+)`)

func presetName(note string) string {
	if m := presetRe.FindStringSubmatch(note); m != nil {
		return m[1]
	}
	return ""
}

// For resolves the effective grade of a sequence item: its named preset (if a
// #grade:name tag names one) with the item's inline params layered on top.
// dir is the project's grades folder.
func For(dir, note string) (*Grade, error) {
	g := New()
	if name := presetName(note); name != "" {
		p, err := Load(dir, name)
		if err != nil {
			return nil, err
		}
		g.merge(p)
	}
	_, inline := SplitNote(note)
	g.merge(inline)
	return g, nil
}

// Load reads grades/<name>.grade (key=value lines, # comments).
func Load(dir, name string) (*Grade, error) {
	path := filepath.Join(dir, name+".grade")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("grade preset %q not found (%s)", name, path)
	}
	g := New()
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", path, line)
		}
		if err := g.Set(strings.TrimSpace(k), f); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return g, nil
}

// Save writes a preset file, with a header listing the parameters so it is
// self-documenting and easy to hand-edit.
func Save(dir, name string, g *Grade) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# movielily grade preset. key=value, one per line.\n")
	b.WriteString("# apply to a scene by tagging its note: #grade:" + name + "\n")
	for _, n := range Names() {
		b.WriteString("# " + n + ": " + params[n].help + "\n")
	}
	b.WriteString("\n")
	for _, n := range Names() {
		if v, ok := g.vals[n]; ok {
			b.WriteString(n + "=" + num(v) + "\n")
		}
	}
	return os.WriteFile(filepath.Join(dir, name+".grade"), []byte(b.String()), 0o644)
}

// Filter compiles the grade to an ffmpeg filter chain (no leading/trailing
// comma), or "" when neutral. Order: tone/color (eq), white balance
// (colortemperature), detail (unsharp), then grain (noise) last so it is not
// smoothed by later stages.
func (g *Grade) Filter() string {
	if g.IsNeutral() {
		return ""
	}
	var f []string

	bri, hasB := g.vals["brightness"]
	con, hasC := g.vals["contrast"]
	sat, hasS := g.vals["saturation"]
	gam, hasG := g.vals["gamma"]
	if hasB || hasC || hasS || hasG {
		// Keys only exist when non-neutral; missing ones fall back to eq's
		// identity (brightness 0, contrast/saturation/gamma 1).
		b, c, s, gm := 0.0, 1.0, 1.0, 1.0
		if hasB {
			b = bri / 100
		}
		if hasC {
			c = con / 100
		}
		if hasS {
			s = sat / 100
		}
		if hasG {
			gm = gam / 100
		}
		f = append(f, fmt.Sprintf("eq=brightness=%s:contrast=%s:saturation=%s:gamma=%s",
			num(b), num(c), num(s), num(gm)))
	}
	if w, ok := g.vals["warmth"]; ok {
		// warmth -100..100 -> ~8500K (cool) .. 4500K (warm); colortemperature
		// raises warmth as K falls.
		k := math.Round(6500 - w*20)
		f = append(f, fmt.Sprintf("colortemperature=temperature=%s", num(k)))
	}
	if s, ok := g.vals["sharpen"]; ok {
		f = append(f, fmt.Sprintf("unsharp=5:5:%s", num(s/100*1.5)))
	}
	if n, ok := g.vals["grain"]; ok {
		// Film grain: temporal noise on the LUMA plane only (c0), so it reads
		// as grain rather than colour speckle and leaves the grade's colour
		// intact.
		f = append(f, fmt.Sprintf("noise=c0s=%s:c0f=t", num(math.Round(n*0.6))))
	}
	return strings.Join(f, ",")
}

// num formats a float compactly (no trailing zeros).
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// GradableNote reports whether an item's kind takes a grade (visual footage,
// not audio beds or sections).
func GradableKind(k model.ItemKind) bool {
	switch k {
	case model.KindVideo, model.KindImage, model.KindTitle, model.KindAnim, model.KindOverlay:
		return true
	}
	return false
}
