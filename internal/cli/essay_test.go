package cli

import (
	"strings"
	"testing"

	"movielily/internal/model"
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
