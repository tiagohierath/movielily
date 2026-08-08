package cli

import (
	"strings"
	"testing"

	"milklily/internal/model"
)

func TestEssayLinesBuildsNarrationAndTimedImages(t *testing.T) {
	cues := []model.Marker{
		{Time: 12, Note: "coin"},
		{Time: 30, Note: "map"},
	}
	lines := essayLines("essay", "audio/dialogue/voice.wav", 45, cues,
		[]string{"images/stills/coin.jpg", "images/stills/map.jpg"})
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"video|audio/dialogue/voice.wav|0|45|narration",
		"overlay|images/stills/coin.jpg|12|18|full|coin",
		"overlay|images/stills/map.jpg|30|15|full|map",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestSilenceSelectKeyIgnoresNoteButKeepsRegion(t *testing.T) {
	a := model.Select{File: "audio/voice.wav", In: 1.04, Out: 9.06, Note: "take 1"}
	b := model.Select{File: "audio/voice.wav", In: 1.0, Out: 9.1, Note: "renamed"}
	c := model.Select{File: "audio/voice.wav", In: 1.0, Out: 9.2, Note: "take 2"}
	if silenceSelectKey(a) != silenceSelectKey(b) {
		t.Fatal("the same rounded silence region should be idempotent")
	}
	if silenceSelectKey(a) == silenceSelectKey(c) {
		t.Fatal("different regions must remain distinct")
	}
}
