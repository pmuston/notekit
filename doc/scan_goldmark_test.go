package doc

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// TestScannerAgreesWithGoldmark cross-checks this package's line scanner against
// goldmark, an independent CommonMark implementation.
//
// goldmark cannot be the scanner itself: it reports an info-less, content-less
// fence with no position at all, and its block spans cover text rather than whole
// lines (see scanBlocks). But it is a good oracle for *which* constructs exist, and
// that is what this test pins — so a divergence in CommonMark interpretation shows
// up here rather than as a mis-spliced notebook.
//
// Front matter is stripped before goldmark sees the bytes, exactly as production
// does. Without that, `---\nnotekit: 1\n---` parses as a thematic break followed by
// a *setext* level-2 heading, inventing a heading that is not in the document.
func TestScannerAgreesWithGoldmark(t *testing.T) {
	for _, f := range fixtures {
		if f.goldmarkDiverges {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			src := []byte(front + f.body)
			n := mustParse(t, string(src))

			mine := scanBlocks(src, n.Body().Start)
			var myFences, myHeadings []string
			for _, b := range mine {
				switch b.kind {
				case blkFence:
					myFences = append(myFences, b.info)
				case blkHeading:
					myHeadings = append(myHeadings, headingKey(b.level, b.text))
				}
			}

			theirFences, theirHeadings := goldmarkView(t, src, n.Body().Start)

			if !equalStrings(myFences, theirFences) {
				t.Errorf("fence info strings differ:\n  scanner  %q\n  goldmark %q", myFences, theirFences)
			}
			if !equalStrings(myHeadings, theirHeadings) {
				t.Errorf("ATX headings differ:\n  scanner  %q\n  goldmark %q", myHeadings, theirHeadings)
			}
		})
	}
}

// goldmarkView returns goldmark's top-level fence info strings and ATX headings.
func goldmarkView(t *testing.T, src []byte, bodyStart int) (fences, headings []string) {
	t.Helper()
	body := src[bodyStart:]
	doc := goldmark.New().Parser().Parse(text.NewReader(body))

	for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
		switch v := node.(type) {
		case *ast.FencedCodeBlock:
			info := ""
			if v.Info != nil {
				info = string(v.Info.Segment.Value(body))
			}
			fences = append(fences, info)

		case *ast.Heading:
			// Setext headings are also ast.Heading but do not begin a section
			// (§4.1), so they are filtered out by re-reading the line: only a
			// line whose first non-space character is '#' is ATX.
			if !isATXLine(body, v) {
				continue
			}
			var htext string
			if lines := v.Lines(); lines != nil && lines.Len() > 0 {
				seg := lines.At(0)
				htext = string(seg.Value(body))
			}
			headings = append(headings, headingKey(v.Level, strings.TrimSpace(trimClosingHashes(htext))))
		}
	}
	return fences, headings
}

// isATXLine reports whether a heading node's source line begins with '#'.
func isATXLine(body []byte, h *ast.Heading) bool {
	lines := h.Lines()
	if lines == nil || lines.Len() == 0 {
		// A heading with no text is `##` alone, which is always ATX.
		return true
	}
	start := lines.At(0).Start
	for start > 0 && body[start-1] != '\n' {
		start--
	}
	for i := start; i < len(body) && body[i] != '\n'; i++ {
		if body[i] == ' ' || body[i] == '\t' {
			continue
		}
		return body[i] == '#'
	}
	return false
}

// trimClosingHashes removes an ATX heading's optional closing sequence, which
// goldmark leaves in the text segment.
func trimClosingHashes(s string) string {
	t := strings.TrimRight(s, " \t\n")
	m := len(t)
	for m > 0 && t[m-1] == '#' {
		m--
	}
	if m == 0 {
		return ""
	}
	if m < len(t) && (t[m-1] == ' ' || t[m-1] == '\t') {
		return strings.TrimRight(t[:m], " \t")
	}
	return t
}

func headingKey(level int, text string) string {
	return strings.Repeat("#", level) + " " + text
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGoldmarkDivergenceIsDocumented records the one known disagreement rather than
// leaving it implicit: a fence indented inside a list item is list content to
// goldmark and a top-level fence to this scanner.
//
// Both readings are defensible and neither corrupts anything — a notebook with a
// fenced cell inside a list item is pathological — but the divergence is real, so it
// is asserted here. If this test starts failing, the scanner's behaviour changed and
// the comment in scanBlocks needs revisiting.
func TestGoldmarkDivergenceIsDocumented(t *testing.T) {
	src := []byte(front + "## H\n\n- item\n  ```sh\n  a\n  ```\n")
	n := mustParse(t, string(src))

	mine := scanBlocks(src, n.Body().Start)
	fences := 0
	for _, b := range mine {
		if b.kind == blkFence {
			fences++
		}
	}
	if fences != 1 {
		t.Errorf("scanner found %d top-level fences, want 1", fences)
	}

	theirFences, _ := goldmarkView(t, src, n.Body().Start)
	if len(theirFences) != 0 {
		t.Errorf("goldmark found %d top-level fences, want 0 (it nests them in the list item)", len(theirFences))
	}

	// The consequence: this scanner sees a cell where goldmark would see none.
	if len(n.Cells()) != 1 {
		t.Errorf("got %d cells, want 1", len(n.Cells()))
	}
}
