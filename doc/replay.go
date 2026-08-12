package doc

import (
	"strings"
	"unicode/utf8"
)

// ReplayRedraws resolves a line that rewrote itself into the text a terminal would
// finally have shown.
//
// A progress display — a spinner, a percentage, a download bar — draws one line over
// and over, returning to column zero with a carriage return between frames. A terminal
// makes that look like one line changing. A file has no cursor, so every frame is still
// there, and dropping the carriage return (which is what a control-character filter
// does) is the worst outcome available: the instruction is gone and the frames it
// governed are not, leaving them run together with the real output buried at the end.
//
// So the edits are replayed rather than removed. Within one line:
//
//   - carriage return moves to column zero
//   - backspace moves back one column
//   - ESC[nG moves to a column, ESC[nC and ESC[nD move right and left
//   - ESC[K (and 0K, 1K, 2K) erase to end of line, to start of line, or all of it
//   - anything else printable is written at the cursor, overwriting what was there
//
// Overwriting column by column is what a terminal actually does, so a frame shorter
// than the one before it leaves the tail of the longer one visible. That looks wrong
// and is: it is why real tools emit ESC[K, and reproducing it faithfully is better than
// inventing a tidier line the user never saw.
//
// Vertical movement is out of scope. A display that redraws several lines by moving the
// cursor up needs a screen to be modelled, not a line, and a screen is a different
// artifact from the stream a notebook records.
//
// A line containing none of these edits is returned byte for byte, escape sequences
// included. That is what lets this run over a body whose colour must survive: only the
// lines that redrew themselves are rewritten, and colour is worth least on exactly
// those.
func ReplayRedraws(s string) string {
	if !strings.ContainsAny(s, "\r\b\x1b") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '\n')
		line := s[i:]
		if j >= 0 {
			line = s[i : i+j]
		}
		// Untouched lines keep their bytes, so a coloured line next to a progress
		// line is not collateral damage.
		if redrawn(line) {
			b.WriteString(replayLine(line))
		} else {
			b.WriteString(line)
		}
		if j < 0 {
			break
		}
		b.WriteByte('\n')
		i += j + 1
	}
	return b.String()
}

// redrawn reports whether a line moved its cursor horizontally, and so has to be
// replayed rather than kept.
//
// The distinction earns its keep: a line that merely carries colour is returned
// untouched, which is what lets this run over a body whose colour must survive. An
// erase on its own does not count — with the cursor at the end of what was written,
// erasing to end of line does nothing.
func redrawn(line string) bool {
	if strings.ContainsAny(line, "\r\b") {
		return true
	}
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			i++
			continue
		}
		seq, n := scanEscape(line[i:])
		i += n
		if len(seq) > 2 && seq[1] == '[' {
			switch seq[len(seq)-1] {
			case 'G', 'C', 'D':
				return true
			}
		}
	}
	return false
}

// replayLine replays one line's cursor movement onto a column buffer.
//
// Columns are runes, not bytes. A byte-wise cursor would let an overwrite land in the
// middle of a multi-byte character and produce invalid UTF-8 — and the spinner frames
// that make this function necessary are themselves multi-byte (U+280B and friends).
// A double-width character still counts as one column, which is wrong for CJK and not
// worth a width table here.
func replayLine(line string) string {
	var cells []rune
	cur := 0

	put := func(r rune) {
		for len(cells) < cur {
			cells = append(cells, ' ')
		}
		if cur < len(cells) {
			cells[cur] = r
		} else {
			cells = append(cells, r)
		}
		cur++
	}

	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '\r':
			cur = 0
			i++
		case c == '\b':
			if cur > 0 {
				cur--
			}
			i++
		case c == 0x1b:
			seq, n := scanEscape(line[i:])
			i += n
			// Only erase-in-line acts. Every other sequence is dropped: a redrawn
			// line is reduced to the text that survived, and re-attaching colour
			// state to columns that several frames wrote in turn would be guessing.
			if col, ok := moveInLine(seq, cur); ok {
				cur = col
				continue
			}
			mode, ok := eraseInLine(seq)
			if !ok {
				continue
			}
			switch mode {
			case 0: // to end of line
				if cur < len(cells) {
					cells = cells[:cur]
				}
			case 1: // to start of line, inclusive
				for k := 0; k < cur && k < len(cells); k++ {
					cells[k] = ' '
				}
			case 2: // the whole line
				cells = cells[:0]
			}
		case c == '\t':
			put('\t')
			i++
		case c < 0x20 || c == 0x7f:
			i++ // other control characters have no column
		default:
			r, n := utf8.DecodeRuneInString(line[i:])
			put(r)
			i += n
		}
	}
	return string(cells)
}

// scanEscape returns the escape sequence at the start of s and its length, using the
// same grammar as [StripANSI] so the two cannot disagree about where a sequence ends.
func scanEscape(s string) (string, int) {
	if len(s) < 2 {
		return s, len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters then a final byte in @–~
		j := 2
		for j < len(s) && (s[j] < '@' || s[j] > '~') {
			j++
		}
		if j < len(s) {
			j++
		}
		return s[:j], j
	case ']': // OSC: terminated by BEL or ESC \
		j := 2
		for j < len(s) {
			if s[j] == 0x07 {
				j++
				break
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				j += 2
				break
			}
			j++
		}
		return s[:j], j
	default: // ESC I… F
		j := 1
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) {
			j++
		}
		return s[:j], j
	}
}

// moveInLine resolves a horizontal cursor sequence against the current column,
// returning the new one. CHA (`ESC[nG`) is absolute and one-based, so `ESC[1G` and a
// carriage return mean the same thing; CUF and CUB (`C` and `D`) are relative. An
// omitted parameter is 1 for all three, per ECMA-48.
func moveInLine(seq string, cur int) (int, bool) {
	if len(seq) < 3 || seq[0] != 0x1b || seq[1] != '[' {
		return 0, false
	}
	n, ok := csiParam(seq[2:len(seq)-1], 1)
	if !ok {
		return 0, false
	}
	switch seq[len(seq)-1] {
	case 'G':
		if n < 1 {
			n = 1
		}
		return n - 1, true
	case 'C':
		return cur + n, true
	case 'D':
		if cur-n < 0 {
			return 0, true
		}
		return cur - n, true
	}
	return 0, false
}

// csiParam parses a single numeric CSI parameter, returning def when it is omitted.
// A multi-parameter sequence is refused rather than guessed at: nothing horizontal
// takes two, so one here means the sequence was not what it looked like.
func csiParam(s string, def int) (int, bool) {
	if s == "" {
		return def, true
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<16 {
			return 0, false // absurd column, treat as not a movement
		}
	}
	return n, true
}

// eraseInLine reports whether seq is CSI n K, and which n. Absent parameters mean 0,
// which is what `ESC[K` on its own is — the sequence a progress display emits after a
// short frame so the previous longer one does not show through.
func eraseInLine(seq string) (int, bool) {
	if len(seq) < 3 || seq[0] != 0x1b || seq[1] != '[' || seq[len(seq)-1] != 'K' {
		return 0, false
	}
	switch seq[2 : len(seq)-1] {
	case "", "0":
		return 0, true
	case "1":
		return 1, true
	case "2":
		return 2, true
	}
	return 0, false
}
