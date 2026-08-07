package ffmpeg

import (
	"strings"
	"testing"

	"movielily/internal/model"
)

func TestGeometryChainImagePanEaseInOut(t *testing.T) {
	it := model.SequenceItem{Kind: model.KindImage, Dur: 2, Note: "#pan_rl #ease_inout"}
	got := geometryChain(it, 1440, 1080, 24)
	for _, want := range []string{
		"force_original_aspect_ratio=increase",
		"crop=1440:1080:(iw-ow)*(1-(0.5-0.5*cos(PI*t/2))):(ih-oh)/2",
		"fps=24",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("geometryChain = %q, missing %q", got, want)
		}
	}
}

func TestGeometryChainImagePanDefaultsLinear(t *testing.T) {
	it := model.SequenceItem{Kind: model.KindImage, Dur: 3, Note: "#pan_tb"}
	got := geometryChain(it, 1440, 1080, 24)
	if !strings.Contains(got, "crop=1440:1080:(iw-ow)/2:(ih-oh)*(t/3)") {
		t.Fatalf("geometryChain = %q, missing top-bottom linear crop", got)
	}
}

func TestGeometryChainImagePanEaseInOutModes(t *testing.T) {
	for _, tc := range []struct {
		note string
		want string
	}{
		{"#pan_lr #ease_in", "crop=1440:1080:(iw-ow)*((t/2)*(t/2)):(ih-oh)/2"},
		{"#pan_lr #ease_out", "crop=1440:1080:(iw-ow)*(1-(1-t/2)*(1-t/2)):(ih-oh)/2"},
	} {
		it := model.SequenceItem{Kind: model.KindImage, Dur: 2, Note: tc.note}
		got := geometryChain(it, 1440, 1080, 24)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("geometryChain(%q) = %q, missing %q", tc.note, got, tc.want)
		}
	}
}
