package grade

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSetAndString(t *testing.T) {
	g := New()
	if err := g.Set("saturation", 120); err != nil {
		t.Fatal(err)
	}
	if err := g.Set("grain", 25); err != nil {
		t.Fatal(err)
	}
	if err := g.Set("sat", 130); err != nil { // alias overwrites
		t.Fatal(err)
	}
	if s := g.String(); s != "saturation=130 grain=25" {
		t.Errorf("String() = %q, want saturation then grain in order", s)
	}
	// Setting a param to its neutral removes it (reversible, minimal text).
	if err := g.Set("saturation", 100); err != nil {
		t.Fatal(err)
	}
	if s := g.String(); s != "grain=25" {
		t.Errorf("neutral value should drop the token, got %q", s)
	}
}

func TestSetRejects(t *testing.T) {
	g := New()
	if err := g.Set("bogus", 1); err == nil {
		t.Error("unknown parameter should error")
	}
	if err := g.Set("saturation", 500); err == nil {
		t.Error("out-of-range value should error")
	}
}

func TestSplitAndMergeNote(t *testing.T) {
	human, g := SplitNote("sunset shot #best saturation=120 grain=25")
	if human != "sunset shot #best" {
		t.Errorf("human = %q, want the note without grade tokens", human)
	}
	if g.String() != "saturation=120 grain=25" {
		t.Errorf("grade = %q", g.String())
	}
	// Round-trip: editing the grade preserves the human text and tags.
	g2 := New()
	_ = g2.Set("contrast", 110)
	out := MergeIntoNote("sunset shot #best saturation=120 grain=25", g2)
	if out != "sunset shot #best contrast=110" {
		t.Errorf("MergeIntoNote = %q", out)
	}
	// Clearing the grade leaves just the human note (fully reversible).
	if out := MergeIntoNote("sunset #best saturation=120", New()); out != "sunset #best" {
		t.Errorf("cleared grade = %q, want human only", out)
	}
}

func TestFilterNeutralEmpty(t *testing.T) {
	if f := New().Filter(); f != "" {
		t.Errorf("neutral grade must produce no filter, got %q", f)
	}
}

func TestFilterChain(t *testing.T) {
	g := New()
	_ = g.Set("saturation", 120)
	_ = g.Set("grain", 50)
	_ = g.Set("warmth", 50)
	f := g.Filter()
	// eq (colour) before colortemperature before noise (grain last).
	iEq := strings.Index(f, "eq=")
	iTemp := strings.Index(f, "colortemperature=")
	iNoise := strings.Index(f, "noise=c0s=")
	if iEq < 0 || iTemp < 0 || iNoise < 0 {
		t.Fatalf("missing a stage in %q", f)
	}
	if !(iEq < iTemp && iTemp < iNoise) {
		t.Errorf("wrong filter order: %q", f)
	}
	if !strings.Contains(f, "saturation=1.2") {
		t.Errorf("saturation not mapped to 1.2: %q", f)
	}
}

func TestPresetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	g := New()
	_ = g.Set("saturation", 115)
	_ = g.Set("grain", 20)
	if err := Save(dir, "filmic", g); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "filmic")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.String() != g.String() {
		t.Errorf("round-trip mismatch: saved %q loaded %q", g.String(), loaded.String())
	}
	if _, err := Load(dir, "missing"); err == nil {
		t.Error("loading a missing preset should error")
	}
	_ = filepath.Join // keep import if unused elsewhere
}

func TestForPresetPlusInline(t *testing.T) {
	dir := t.TempDir()
	base := New()
	_ = base.Set("saturation", 110)
	_ = base.Set("grain", 10)
	_ = Save(dir, "look", base)
	// #grade:look supplies the preset; the inline grain=40 overrides it.
	g, err := For(dir, "shot #grade:look grain=40")
	if err != nil {
		t.Fatal(err)
	}
	if g.String() != "saturation=110 grain=40" {
		t.Errorf("For = %q, want inline overriding preset", g.String())
	}
}
