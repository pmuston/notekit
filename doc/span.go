package doc

import "bytes"

// Span is a half-open byte range [Start, End) in a notebook's source.
//
// Every construct the parser recognises records its exact span, which is what
// makes byte-range splice — the only write path — possible (§10).
type Span struct {
	Start, End int
}

// Len returns the span's length in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Empty reports whether the span covers no bytes. An empty span is still a
// position: it is where an insertion happens.
func (s Span) Empty() bool { return s.End <= s.Start }

// In returns the bytes the span covers.
func (s Span) In(src []byte) []byte { return src[s.Start:s.End] }

// lineEnd returns the index just past the newline ending the line beginning at p,
// or len(src) when the final line is unterminated.
func lineEnd(src []byte, p int) int {
	if i := bytes.IndexByte(src[p:], '\n'); i >= 0 {
		return p + i + 1
	}
	return len(src)
}

// lineText returns a line's content without its trailing newline or carriage
// return, so line matchers never have to account for either.
func lineText(src []byte, start, end int) []byte {
	t := src[start:end]
	t = bytes.TrimSuffix(t, []byte("\n"))
	return bytes.TrimSuffix(t, []byte("\r"))
}

func isBlank(line []byte) bool {
	for _, c := range line {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
