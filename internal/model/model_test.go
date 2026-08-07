package model

import (
	"reflect"
	"testing"
)

func TestFormatSeconds(t *testing.T) {
	for in, want := range map[float64]string{72.3: "72.3", 5.0: "5", 85.1: "85.1", 0: "0", 12.0: "12"} {
		if got := FormatSeconds(in); got != want {
			t.Errorf("FormatSeconds(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"72.3", 72.3}, {"5", 5}, {"5s", 5}, {"1:12.5", 72.5}, {"1:02:03", 3723},
	}
	for _, c := range cases {
		got, err := ParseSeconds(c.in)
		if err != nil {
			t.Fatalf("ParseSeconds(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseSeconds(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := ParseSeconds("nope"); err == nil {
		t.Error("ParseSeconds(\"nope\"): expected error")
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	m := Marker{File: "clip001.mp4", Time: 72.3, Note: "funny reaction #funny"}
	got, err := ParseMarker(m.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("round trip %q -> %+v, want %+v", m.String(), got, m)
	}
}

// Selects/markers keep the final note field intact even when it contains '|'.
func TestSelectRoundTripPipeAndUnicode(t *testing.T) {
	s := Select{File: "clip 001.mp4", In: 1.5, Out: 2, Note: "great | reaction — fünny #best"}
	got, err := ParseSelect(s.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round trip %q -> %+v, want %+v", s.String(), got, s)
	}
}

func TestItemRoundTrip(t *testing.T) {
	for _, it := range []SequenceItem{
		{Kind: KindVideo, File: "clip001.mp4", In: 72.3, Out: 85.1, Note: "great reaction"},
		{Kind: KindImage, File: "phòto.jpg", Dur: 5, Note: "opening image #intro"},
		{Kind: KindSection, Note: "Scene 1 — the arrival"},
	} {
		got, err := ParseItem(it.String())
		if err != nil {
			t.Fatalf("ParseItem(%q): %v", it.String(), err)
		}
		if got != it {
			t.Errorf("round trip %q -> %+v, want %+v", it.String(), got, it)
		}
	}
}

func TestNoteAnchorOptional(t *testing.T) {
	for _, n := range []Note{
		{Text: "general thought"},
		{File: "a.mp4", Time: 10, HasTime: true, Text: "use for intro"},
	} {
		got, err := ParseNote(n.String())
		if err != nil {
			t.Fatalf("ParseNote(%q): %v", n.String(), err)
		}
		if got != n {
			t.Errorf("round trip %q -> %+v, want %+v", n.String(), got, n)
		}
	}
}

func TestTags(t *testing.T) {
	got := Tags("good #Reaction and #funny #funny #b-roll")
	want := []string{"#reaction", "#funny", "#b-roll"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
}

func TestImagePan(t *testing.T) {
	spec, ok := ImagePan("wide board #pan_rl #ease_inout")
	if !ok {
		t.Fatal("ImagePan did not find #pan_rl")
	}
	if spec.Direction != PanRightLeft || spec.Ease != PanEaseInOut {
		t.Fatalf("ImagePan = %+v, want right-left ease-in/out", spec)
	}
	if spec.Label() != "pan R->L ease in/out" {
		t.Fatalf("Label = %q", spec.Label())
	}

	spec, ok = ImagePan("wide board #pan_lr #ease_out")
	if !ok {
		t.Fatal("ImagePan did not find #pan_lr")
	}
	if spec.Direction != PanLeftRight || spec.Ease != PanEaseOut {
		t.Fatalf("ImagePan = %+v, want left-right ease-out", spec)
	}
	if spec.Label() != "pan L->R ease out" {
		t.Fatalf("Label = %q", spec.Label())
	}

	spec, ok = ImagePan("tall board #pan_tb")
	if !ok {
		t.Fatal("ImagePan did not find #pan_tb")
	}
	if spec.Direction != PanTopBottom || spec.Ease != PanEaseLinear {
		t.Fatalf("ImagePan default = %+v, want top-bottom linear", spec)
	}

	if _, ok := ImagePan("still board #ease_inout"); ok {
		t.Fatal("ImagePan found a pan without a pan direction tag")
	}
}

func TestSetImagePan(t *testing.T) {
	got := SetImagePan("wide shot #pan_lr #ease_linear #keep", PanSpec{Direction: PanBottomTop, Ease: PanEaseIn})
	want := "wide shot #keep #pan_bt #ease_in"
	if got != want {
		t.Fatalf("SetImagePan = %q, want %q", got, want)
	}
	if got := SetImagePan("wide shot #pan_rl #ease_out #keep", PanSpec{}); got != "wide shot #keep" {
		t.Fatalf("SetImagePan clear = %q", got)
	}
}
