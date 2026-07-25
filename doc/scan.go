package doc

import (
	"bytes"
	"strings"

	"github.com/pmuston/notekit/meta"
)

// provenancePrefix marks an HTML comment as a notekit result reference (§8).
// Requiring it is what keeps ordinary HTML comments — which users write — from
// being mistaken for tool-written provenance.
const provenancePrefix = "notekit:result"

type blockKind uint8

const (
	blkBlank blockKind = iota
	blkProse
	blkHeading
	blkFence
	blkComment // provenance marker (§8)
	blkImage
)

// block is one scanned construct with its exact line-aligned span.
//
// Every block except a fence covers exactly one line. A fence covers its opening
// line, its body, and its closing line — or the rest of the file when unclosed,
// which is what CommonMark does.
type block struct {
	kind blockKind
	span Span

	level int    // blkHeading: 1–6
	text  string // blkHeading: heading text with any closing #'s removed

	infoSpan Span   // blkFence: the info string, excluding fence characters and newline
	info     string // blkFence: info string source
	tag      string // blkFence: info string tag
	body     Span   // blkFence: body, excluding both fence lines
	closed   bool   // blkFence: false when the fence is unterminated at end of file

	attrs string // blkComment: text after the provenance prefix
	dest  string // blkImage: link destination
}

// scanBlocks scans the document body into line-aligned blocks.
//
// This is a purpose-built line scanner rather than a walk of goldmark's AST, for
// two reasons found by probing goldmark 1.8.4:
//
//   - An info-less, content-less fence (```` ```\n``` ````) is reported as a node
//     with no position information at all, so its span cannot be recovered. Such a
//     fence still matters: as a section's first fence it suppresses the cell
//     (§4.3), so missing it would make a later fence look like the source and let a
//     tool run the wrong bytes.
//   - Block nodes carry text spans, not line spans (a `## H` heading reports only
//     "H"), so every span needs line-snapping regardless.
//
// Tracking fence state by line also gives the "is this heading inside a fence?"
// question a single obvious answer, where reconstructing it from an AST would
// depend on the spans goldmark cannot supply. goldmark remains in the build as an
// independent CommonMark oracle: scan_goldmark_test.go asserts this scanner agrees
// with it on every fixture.
func scanBlocks(src []byte, start int) []block {
	var out []block
	for p := start; p < len(src); {
		e := lineEnd(src, p)
		line := lineText(src, p, e)

		if ch, n, infoAt, ok := fenceOpener(line); ok {
			b := scanFence(src, p, e, line, ch, n, infoAt)
			out = append(out, b)
			p = b.span.End
			continue
		}

		var b block
		switch {
		case isBlank(line):
			b = block{kind: blkBlank}
		case matchHeading(line) != nil:
			h := matchHeading(line)
			b = block{kind: blkHeading, level: h.level, text: h.text}
		case hasProvenance(line):
			b = block{kind: blkComment, attrs: provenanceAttrs(line)}
		case imageDest(line) != "":
			b = block{kind: blkImage, dest: imageDest(line)}
		default:
			b = block{kind: blkProse}
		}
		b.span = Span{Start: p, End: e}
		out = append(out, b)
		p = e
	}
	return out
}

// scanFence consumes a fenced code block starting at its opening line.
func scanFence(src []byte, p, e int, line []byte, ch byte, n, infoAt int) block {
	b := block{kind: blkFence}
	b.span.Start = p
	b.infoSpan = Span{Start: p + infoAt, End: p + len(line)}
	b.info = string(src[b.infoSpan.Start:b.infoSpan.End])
	b.tag = meta.Tag(b.info)
	b.body.Start = e

	q := e
	for q < len(src) {
		qe := lineEnd(src, q)
		if fenceCloser(lineText(src, q, qe), ch, n) {
			b.body.End = q
			b.span.End = qe
			b.closed = true
			return b
		}
		q = qe
	}
	// Unclosed: the body, and the block, run to end of file — which is what
	// CommonMark does, and which leaves the cell no result position (§4.2).
	b.body.End = len(src)
	b.span.End = len(src)
	return b
}

// indentOf returns the count of leading spaces, capped at 4. CommonMark allows a
// block construct up to three spaces of indent; four makes it indented code.
func indentOf(line []byte) int {
	i := 0
	for i < len(line) && i < 4 && line[i] == ' ' {
		i++
	}
	return i
}

// fenceOpener reports whether line opens a fenced code block, returning the fence
// character, its run length, and the offset at which the info string begins.
func fenceOpener(line []byte) (ch byte, n, infoAt int, ok bool) {
	i := indentOf(line)
	if i > 3 || i >= len(line) {
		return 0, 0, 0, false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return 0, 0, 0, false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	if j-i < 3 {
		return 0, 0, 0, false
	}
	// A backtick fence's info string may not contain a backtick (CommonMark),
	// which is what keeps inline code spans from opening fences.
	if c == '`' && bytes.IndexByte(line[j:], '`') >= 0 {
		return 0, 0, 0, false
	}
	return c, j - i, j, true
}

// fenceCloser reports whether line closes a fence opened with n of ch.
func fenceCloser(line []byte, ch byte, n int) bool {
	i := indentOf(line)
	if i > 3 {
		return false
	}
	j := i
	for j < len(line) && line[j] == ch {
		j++
	}
	if j-i < n {
		return false
	}
	// Only trailing whitespace may follow a closing fence.
	return isBlank(line[j:])
}

type heading struct {
	level int
	text  string
}

// matchHeading matches an ATX heading, returning nil when line is not one.
//
// Setext headings are deliberately not matched: §4 admits ATX headings only, so an
// underlined title does not begin a section.
func matchHeading(line []byte) *heading {
	i := indentOf(line)
	if i > 3 {
		return nil
	}
	j := i
	for j < len(line) && line[j] == '#' {
		j++
	}
	level := j - i
	if level < 1 || level > 6 {
		return nil
	}
	// The hashes must be followed by whitespace or end of line, else it is text.
	if j < len(line) && line[j] != ' ' && line[j] != '\t' {
		return nil
	}

	t := bytes.TrimSpace(line[j:])
	// Strip an optional closing sequence of #'s (CommonMark).
	if k := len(t); k > 0 {
		m := k
		for m > 0 && t[m-1] == '#' {
			m--
		}
		switch {
		case m == 0:
			t = nil
		case m < k && (t[m-1] == ' ' || t[m-1] == '\t'):
			t = bytes.TrimRight(t[:m], " \t")
		}
	}
	return &heading{level: level, text: string(t)}
}

// hasProvenance reports whether line is a notekit result provenance comment (§8).
func hasProvenance(line []byte) bool {
	_, ok := provenanceBody(line)
	return ok
}

func provenanceAttrs(line []byte) string {
	s, _ := provenanceBody(line)
	return s
}

func provenanceBody(line []byte) (string, bool) {
	t := bytes.TrimSpace(line)
	if !bytes.HasPrefix(t, []byte("<!--")) || !bytes.HasSuffix(t, []byte("-->")) || len(t) < 7 {
		return "", false
	}
	inner := bytes.TrimSpace(t[4 : len(t)-3])
	if !bytes.HasPrefix(inner, []byte(provenancePrefix)) {
		return "", false
	}
	rest := inner[len(provenancePrefix):]
	// The prefix must be a whole token: `notekit:results` is not a marker.
	if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return string(bytes.TrimSpace(rest)), true
}

// imageDest returns the destination of a line consisting solely of an image link,
// or "" when the line is something else.
//
// Recognising the image is not enough to make it a result: §8 requires a
// provenance comment immediately before it, and that check lives in cell assembly.
// A false positive here is therefore harmless.
func imageDest(line []byte) string {
	t := bytes.TrimSpace(line)
	if !bytes.HasPrefix(t, []byte("![")) || !bytes.HasSuffix(t, []byte(")")) {
		return ""
	}
	i := bytes.Index(t, []byte("]("))
	if i < 0 {
		return ""
	}
	dest := strings.TrimSpace(string(t[i+2 : len(t)-1]))
	// A title, when present, follows the destination after whitespace.
	if j := strings.IndexAny(dest, " \t"); j >= 0 {
		dest = dest[:j]
	}
	return dest
}
