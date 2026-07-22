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

// The canonical parameters, in a natural grading order. Neutral = "do nothing".
var params = map[string]param{
	"brightness":  {"brightness", 0, 0, -100, 100, "exposure: -100 dark … 0 … 100 bright"},
	"contrast":    {"contrast", 1, 100, 0, 200, "0 flat … 100 normal … 200 punchy"},
	"temperature": {"temperature", 2, 0, -100, 100, "-100 cool/blue … 0 … 100 warm/orange"},
	"tint":        {"tint", 3, 0, -100, 100, "-100 green … 0 … 100 magenta"},
	"highlights":  {"highlights", 4, 0, -100, 100, "recover/brighten the bright end"},
	"shadows":     {"shadows", 5, 0, -100, 100, "lift/deepen the dark end"},
	"saturation":  {"saturation", 6, 100, 0, 200, "0 grey … 100 normal … 200 vivid"},
	"vibrance":    {"vibrance", 7, 0, -100, 100, "smart saturation (spares skin/strong colours)"},
	"blackpoint":  {"blackpoint", 8, 0, -100, 100, "-100 crush blacks … 0 … 100 lift blacks"},
	"whitepoint":  {"whitepoint", 9, 0, -100, 100, "-100 dim whites … 0 … 100 (clips at pure white)"},
	"grain":       {"grain", 10, 0, 0, 100, "0 clean … 100 heavy film grain"},
	"bloom":       {"bloom", 11, 0, 0, 100, "glow bleeding from the highlights"},
	"sharpen":     {"sharpen", 12, 0, 0, 100, "0 none … 100 crisp"},
	"vignette":    {"vignette", 13, 0, 0, 100, "darken toward the corners"},
	"fade":        {"fade", 14, 0, 0, 100, "lift blacks to grey for a matte look"},
}

// aliases map friendly short/other names onto canonical ones.
var aliases = map[string]string{
	"exposure": "brightness", "bri": "brightness", "exp": "brightness",
	"con":    "contrast",
	"warmth": "temperature", "temp": "temperature", "warm": "temperature",
	"hi": "highlights", "highlight": "highlights", "high": "highlights",
	"sh": "shadows", "shadow": "shadows",
	"sat":   "saturation",
	"vib":   "vibrance",
	"black": "blackpoint", "blacks": "blackpoint", "bp": "blackpoint",
	"white": "whitepoint", "whites": "whitepoint", "wp": "whitepoint",
	"noise": "grain", "grains": "grain",
	"glow":  "bloom",
	"sharp": "sharpen", "sharpness": "sharpen",
	"vig":  "vignette",
	"lift": "fade", "matte": "fade",
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
				if err := g.Set(m[1], v); err == nil {
					continue // valid grade token: pull it out of the human text
				}
				// out-of-range or otherwise invalid: leave it visible so the
				// user sees (and can fix) it rather than it vanishing silently.
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

// presetRe pulls the "#grade:name" tag from a note, if present.
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
	fmt.Fprintf(&b, "# movielily grade preset. key=value, one per line.\n")
	fmt.Fprintf(&b, "# apply to a scene by tagging its note: #grade:%s\n", name)
	for _, n := range Names() {
		fmt.Fprintf(&b, "# %s: %s\n", n, params[n].help)
	}
	b.WriteString("\n")
	for _, n := range Names() {
		if v, ok := g.vals[n]; ok {
			fmt.Fprintf(&b, "%s=%s\n", n, num(v))
		}
	}
	return os.WriteFile(filepath.Join(dir, name+".grade"), []byte(b.String()), 0o644)
}

// Filter compiles the grade to a LINEAR ffmpeg filter chain (single in, single
// out; no leading/trailing comma), or "" when neutral. Bloom is not here — it
// needs a split/blend sub-graph, exposed separately via Bloom(). Order runs
// from tone/colour through detail to grain last (so grain is not smoothed).
func (g *Grade) Filter() string {
	if g.IsNeutral() {
		return ""
	}
	var f []string

	// eq: exposure, contrast, saturation (all multiplicative/additive tone).
	bri, hasB := g.vals["brightness"]
	con, hasC := g.vals["contrast"]
	sat, hasS := g.vals["saturation"]
	if hasB || hasC || hasS {
		b, c, s := 0.0, 1.0, 1.0
		if hasB {
			b = bri / 100 * 0.5 // ±0.5 exposure at the extremes
		}
		if hasC {
			c = con / 100
		}
		if hasS {
			s = sat / 100
		}
		f = append(f, fmt.Sprintf("eq=brightness=%s:contrast=%s:saturation=%s", num(b), num(c), num(s)))
	}
	// White balance and tint.
	if w, ok := g.vals["temperature"]; ok {
		k := math.Round(6500 - w*20) // warm as K falls
		f = append(f, fmt.Sprintf("colortemperature=temperature=%s", num(k)))
	}
	if t, ok := g.vals["tint"]; ok {
		// green↔magenta on the midtones. Positive tint = magenta = less green.
		f = append(f, fmt.Sprintf("colorbalance=gm=%s", num(-t/100*0.4)))
	}
	// Tonal curve: highlights, shadows, black/white point, fade all shape one
	// curve on [0,1] with anchors at 0, ¼, ¾, 1.
	if c := g.toneCurve(); c != "" {
		f = append(f, c)
	}
	if v, ok := g.vals["vibrance"]; ok {
		f = append(f, fmt.Sprintf("vibrance=intensity=%s", num(v/100)))
	}
	if s, ok := g.vals["sharpen"]; ok {
		f = append(f, fmt.Sprintf("unsharp=5:5:%s", num(s/100*1.5)))
	}
	if v, ok := g.vals["vignette"]; ok && v > 0 {
		a := math.Pi / 2 * (1 - v/100*0.85) // smaller angle = stronger
		f = append(f, fmt.Sprintf("vignette=angle=%s", num(round3(a))))
	}
	if n, ok := g.vals["grain"]; ok {
		// Temporal noise on the LUMA plane only, so it reads as film grain and
		// leaves the grade's colour intact.
		f = append(f, fmt.Sprintf("noise=c0s=%s:c0f=t", num(math.Round(n*0.6))))
	}
	return strings.Join(f, ",")
}

// toneCurve builds a `curves` filter from the tonal params, or "" if none set.
func (g *Grade) toneCurve() string {
	hi, hasHi := g.vals["highlights"]
	sh, hasSh := g.vals["shadows"]
	bp, hasBp := g.vals["blackpoint"]
	wp, hasWp := g.vals["whitepoint"]
	fd, hasFd := g.vals["fade"]
	if !hasHi && !hasSh && !hasBp && !hasWp && !hasFd {
		return ""
	}
	y0 := clamp01(fd/100*0.18 + bp/100*0.15) // black output: fade + black lift
	ysh := clamp01(0.25 + sh/100*0.18)
	yhi := clamp01(0.75 + hi/100*0.18)
	y1 := clamp01(1 + wp/100*0.15) // whitepoint <0 dims whites; >0 clamps at 1
	return fmt.Sprintf("curves=all=%s", strconv.Quote(fmt.Sprintf("0/%s 0.25/%s 0.75/%s 1/%s",
		num(round3(y0)), num(round3(ysh)), num(round3(yhi)), num(round3(y1)))))
}

// Bloom returns the highlight-glow parameters, if bloom is set. sigma is the
// blur radius and opacity how strongly the glow is screened back on. It is a
// sub-graph (split/blend), so the export builds it around the linear chain.
func (g *Grade) Bloom() (sigma, opacity float64, ok bool) {
	v, ok := g.vals["bloom"]
	if !ok || v <= 0 {
		return 0, 0, false
	}
	return round3(8 + v/100*20), round3(v / 100 * 0.6), true
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// num formats a float compactly (no trailing zeros).
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// GradableKind reports whether an item's kind takes a grade. Overlays are
// composited separately (not through the scene chain), so their grade would
// be ignored — they are deliberately excluded.
func GradableKind(k model.ItemKind) bool {
	switch k {
	case model.KindVideo, model.KindImage, model.KindTitle, model.KindAnim:
		return true
	}
	return false
}
