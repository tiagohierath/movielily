package tui

import "testing"

// visTrunc is what keeps a styled right-pane label from wrapping onto — and
// corrupting — the left pane, so it has to measure width by visible cells while
// letting ANSI SGR escapes through untouched.
func TestVisWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"\x1b[33m#best\x1b[0m", 5},          // escapes don't count
		{"\x1b[1;36m▌ COLD OPEN\x1b[0m", 11}, // multi-param SGR + box glyph
		{"a\x1b[2mb\x1b[0mc", 3},             // escapes interleaved with text
	}
	for _, c := range cases {
		if got := visWidth(c.in); got != c.want {
			t.Errorf("visWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestVisTrunc(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
		want int // expected visible width of the result
	}{
		{"fits untouched", "hello", 10, 5},
		{"exact width", "hello", 5, 5},
		{"plain truncates with ellipsis", "hello world", 5, 5},
		{"width zero is empty", "hello", 0, 0},
		{"styled stays within width", "\x1b[33m#hashtag\x1b[0m", 4, 4},
	}
	for _, c := range cases {
		got := visTrunc(c.in, c.w)
		if w := visWidth(got); w != c.want {
			t.Errorf("%s: visWidth(visTrunc(%q,%d)) = %d, want %d (got %q)", c.name, c.in, c.w, w, c.want, got)
		}
		if w := visWidth(got); w > c.w {
			t.Errorf("%s: visTrunc(%q,%d) overflows width: %d > %d", c.name, c.in, c.w, w, c.w)
		}
	}
}
