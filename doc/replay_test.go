package doc

import "testing"

func TestReplayRedraws(t *testing.T) {
	esc := "\x1b"
	cases := []struct{ name, in, want string }{
		// The case this exists for: a spinner redrawing one line. Every frame is the
		// same width, so the last one wins outright.
		{"spinner", "\r⠋ [1/6] Fetch\r⠙ [2/6] Fetch\r⠹ [3/6] Fetch", "⠹ [3/6] Fetch"},

		// Multi-byte frames must not be cut mid-rune: columns are runes, not bytes.
		{"multibyte overwrite", "⠋⠙⠹\r⠿", "⠿⠙⠹"},

		{"simple overwrite", "abcdef\rXY", "XYcdef"},
		{"erase to end of line", "abcdef\rXY" + esc + "[K", "XY"},
		{"erase explicit 0", "abcdef\rXY" + esc + "[0K", "XY"},
		{"erase whole line", "abcdef" + esc + "[2K\rhi", "hi"},
		{"erase to start", "abcdef\r" + esc + "[2C" + esc + "[1K", "  cdef"},
		{"backspace", "abc\b\bX", "aXc"},
		{"backspace at column zero", "\babc", "abc"},

		// CRLF is the pty's own line ending, not a redraw. Losing it would join
		// every line of ordinary output into one.
		{"crlf", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"crlf with a redraw between", "a\rb\r\nc\r\n", "b\nc\n"},

		// A line that never redrew is returned byte for byte, colour included.
		{"untouched line keeps colour", esc + "[31mred" + esc + "[0m\nplain\n",
			esc + "[31mred" + esc + "[0m\nplain\n"},
		{"no redraw anywhere is identity", "plain text\nsecond\n", "plain text\nsecond\n"},

		// Only the redrawn line is rewritten; its neighbours are untouched.
		{"mixed", esc + "[32mkeep" + esc + "[0m\nold\rnew\ntail\n",
			esc + "[32mkeep" + esc + "[0m\nnew\ntail\n"},

		// A redrawn line loses its colour, which is the documented trade.
		{"colour on a redrawn line", esc + "[31mold\rnew", "new"},

		{"trailing cr with no rewrite", "abc\r", "abc"},
		{"empty", "", ""},
		{"tab holds a column", "ab\tc\rX", "Xb\tc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReplayRedraws(c.in); got != c.want {
				t.Errorf("ReplayRedraws(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// TestReplayRedrawsKeepsUTF8Valid: the spinner characters that make this necessary are
// three bytes each, and a byte-wise cursor would split one.
func TestReplayRedrawsKeepsUTF8Valid(t *testing.T) {
	for _, in := range []string{
		"⠋ [1/6]\r⠙ [2/6]\r⠹ [3/6]",
		"日本語\rab",
		"\r⠧\r⠇\r⠏",
	} {
		got := ReplayRedraws(in)
		for i, r := range got {
			if r == 0xfffd {
				t.Errorf("ReplayRedraws(%q) = %q — invalid UTF-8 at byte %d", in, got, i)
				break
			}
		}
	}
}

// TestStripANSIReplaysFirst: the two must compose, or erase-in-line would be deleted as
// an escape sequence before it had a chance to act.
func TestStripANSIReplaysFirst(t *testing.T) {
	esc := "\x1b"
	cases := []struct{ name, in, want string }{
		{"spinner then output", "\r⠋ [1/6] Fetch\r⠙ [2/6] Fetch" + esc + "[K\nhello\n",
			"⠙ [2/6] Fetch\nhello\n"},
		{"erase honoured, not stripped", "Downloading 100%\rDone" + esc + "[K\n", "Done\n"},
		{"colour still stripped", esc + "[31mred" + esc + "[0m\n", "red\n"},
		{"crlf still becomes lf", "a\r\nb\r\n", "a\nb\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripANSI(c.in); got != c.want {
				t.Errorf("StripANSI(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// TestReplayRedrawsHorizontalMovement covers the tools that use CHA rather than a
// carriage return to get back to column one.
func TestReplayRedrawsHorizontalMovement(t *testing.T) {
	esc := "\x1b"
	cases := []struct{ name, in, want string }{
		{"cha to column one", "old text" + esc + "[1Gnew", "new text"},
		{"cha with erase", "old text" + esc + "[1Gnew" + esc + "[K", "new"},
		{"cha default parameter", "abcdef" + esc + "[GXY", "XYcdef"},
		{"cursor back", "abcdef" + esc + "[3DXYZ", "abcXYZ"},
		{"cursor back past the start clamps", "abc" + esc + "[9DZ", "Zbc"},
		{"cursor forward pads", "ab" + esc + "[2CZ", "ab  Z"},
		// Colour alone is not a redraw, so the line keeps its escapes.
		{"sgr is not movement", esc + "[31mred" + esc + "[0m", esc + "[31mred" + esc + "[0m"},
		// An erase with the cursor already at the end does nothing, so no replay.
		{"erase alone is not a redraw", "line" + esc + "[K", "line" + esc + "[K"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReplayRedraws(c.in); got != c.want {
				t.Errorf("ReplayRedraws(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

// TestReplayRedrawsIsIdempotent: the durable path and the live path both apply it, and
// a body read back from a notebook is replayed again on render.
func TestReplayRedrawsIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"\r⠋ [1/6]\r⠙ [2/6]\x1b[K\nhello\n",
		"50%\r75%\r100%\n",
		"\x1b[31mred\x1b[0m\nplain\n",
		"one\r\ntwo\r\n",
	} {
		once := ReplayRedraws(in)
		if twice := ReplayRedraws(once); twice != once {
			t.Errorf("ReplayRedraws is not idempotent for %q:\n once %q\ntwice %q", in, once, twice)
		}
	}
}
