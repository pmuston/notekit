package kind

import (
	"strconv"
	"strings"
)

// ANSIToHTML converts SGR colour and style escapes into nested spans, HTML-escaping
// everything else.
//
// This is the live half of harvest F12: colour is rendered in the browser and stripped
// from disk, so the durable form is the plain form. Running it over already-stripped
// text is a no-op beyond escaping, which is what lets one renderer serve both a fresh
// result and one read back from a notebook.
//
// Only SGR (`ESC [ … m`) is interpreted. Every other escape sequence is dropped rather
// than rendered: cursor movement and screen clearing have no meaning in a document, and
// emitting them as text would be worse than losing them.
func ANSIToHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)

	open := 0 // currently open <span> elements
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			// Byte-wise, not rune-wise: converting a single byte to a string
			// would reinterpret it as a rune, so 0xE6 would become "æ" and every
			// multi-byte character would be corrupted. Only the five HTML-special
			// characters are ASCII, so escaping them by hand is both correct and
			// UTF-8 safe.
			switch c {
			case '\t', '\n':
				b.WriteByte(c)
			case '&':
				b.WriteString("&amp;")
			case '<':
				b.WriteString("&lt;")
			case '>':
				b.WriteString("&gt;")
			case '"':
				b.WriteString("&#34;")
			case '\'':
				b.WriteString("&#39;")
			default:
				if c >= 0x20 {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}

		// An escape: find its extent, then decide whether it says anything visual.
		if i+1 >= len(s) {
			break // a lone trailing ESC
		}
		if s[i+1] != '[' {
			i += escapeLen(s, i)
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < '@' || s[j] > '~') {
			j++
		}
		if j >= len(s) {
			break // unterminated CSI
		}
		final := s[j]
		params := s[i+2 : j]
		i = j + 1

		if final != 'm' {
			continue // not SGR: no visual meaning in a document
		}
		classes, reset := sgrClasses(params)
		if reset {
			for ; open > 0; open-- {
				b.WriteString("</span>")
			}
		}
		if len(classes) > 0 {
			b.WriteString(`<span class="`)
			b.WriteString(strings.Join(classes, " "))
			b.WriteString(`">`)
			open++
		}
	}
	for ; open > 0; open-- {
		b.WriteString("</span>")
	}
	return b.String()
}

// escapeLen returns the byte length of a non-CSI escape sequence starting at i.
func escapeLen(s string, i int) int {
	if i+1 >= len(s) {
		return 1
	}
	if s[i+1] == ']' { // OSC, terminated by BEL or ST
		j := i + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return j - i + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j - i + 2
			}
			j++
		}
		return len(s) - i
	}
	// ESC I… F: intermediates then one final byte.
	j := i + 1
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j < len(s) {
		j++
	}
	return j - i
}

// sgrClasses maps an SGR parameter list to CSS class names, reporting whether the
// sequence resets existing styling.
//
// Classes rather than inline styles, so a tool restyles the palette in CSS without
// touching Go. An empty or `0` parameter list is a full reset, per the standard.
func sgrClasses(params string) (classes []string, reset bool) {
	if params == "" {
		return nil, true
	}
	for _, p := range strings.Split(params, ";") {
		n, err := strconv.Atoi(p)
		if err != nil {
			continue // unparseable parameter: ignore rather than guess
		}
		switch {
		case n == 0:
			reset = true
			classes = nil
		case n == 1:
			classes = append(classes, "ansi-bold")
		case n == 3:
			classes = append(classes, "ansi-italic")
		case n == 4:
			classes = append(classes, "ansi-underline")
		case n == 9:
			classes = append(classes, "ansi-strike")
		case n == 22, n == 23, n == 24, n == 29, n == 39, n == 49:
			reset = true
			classes = nil
		case n >= 30 && n <= 37:
			classes = append(classes, "ansi-fg-"+strconv.Itoa(n-30))
		case n >= 90 && n <= 97:
			classes = append(classes, "ansi-fg-"+strconv.Itoa(n-90+8))
		case n >= 40 && n <= 47:
			classes = append(classes, "ansi-bg-"+strconv.Itoa(n-40))
		case n >= 100 && n <= 107:
			classes = append(classes, "ansi-bg-"+strconv.Itoa(n-100+8))
		}
	}
	return classes, reset
}
