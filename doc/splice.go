package doc

import (
	"fmt"
	"sort"
	"strings"
)

// Edit replaces one byte range with new text. An edit with an empty span is an
// insertion at that position.
type Edit struct {
	Span Span
	Text string
}

// Apply splices edits into the notebook and returns the new bytes.
//
// This is the only write path (§10). Edits may be supplied in any order but must
// not overlap; every byte outside them is copied through untouched, which is what
// makes round-trip identity hold for everything a tool did not write.
//
// Applying no edits returns the original bytes, so a no-op run cannot perturb a
// file.
func (n *Notebook) Apply(edits ...Edit) ([]byte, error) {
	if len(edits) == 0 {
		return n.src, nil
	}
	sorted := make([]Edit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Span.Start != sorted[j].Span.Start {
			return sorted[i].Span.Start < sorted[j].Span.Start
		}
		return sorted[i].Span.End < sorted[j].Span.End
	})

	for i, e := range sorted {
		if e.Span.Start < 0 || e.Span.End > len(n.src) || e.Span.Start > e.Span.End {
			return nil, fmt.Errorf("notekit: edit span %v is out of bounds for %d bytes", e.Span, len(n.src))
		}
		if i > 0 && e.Span.Start < sorted[i-1].Span.End {
			return nil, fmt.Errorf("notekit: overlapping edits at %v and %v", sorted[i-1].Span, e.Span)
		}
	}

	var b strings.Builder
	b.Grow(len(n.src))
	prev := 0
	for _, e := range sorted {
		b.Write(n.src[prev:e.Span.Start])
		b.WriteString(e.Text)
		prev = e.Span.End
	}
	b.Write(n.src[prev:])
	return []byte(b.String()), nil
}

// SetResult returns the edit that replaces the whole of the cell's result position
// with text (§4.2, §6: results are volatile).
//
// text is the result construct itself, without surrounding blank lines; the
// separating blank line is supplied here so that the same call works whether or not
// the cell already had results. Passing "" clears the result position.
//
// It fails when the source fence is unclosed. Such a fence extends to end of file,
// so there is no position after it: writing there would append into the fence body,
// and closing the fence first would be the silent repair §10 forbids. The cell can
// still be read and run — only persisting its result is impossible until the author
// closes the fence.
func (c *Cell) SetResult(text string) (Edit, error) {
	if !c.Closed {
		return Edit{}, fmt.Errorf(
			"notekit: cell %q has an unclosed source fence, so it has no result position; close the fence to persist results",
			c.HeadingText)
	}
	t := strings.Trim(text, "\n")
	if t == "" {
		return Edit{Span: c.ResultPos, Text: ""}, nil
	}
	// A leading newline creates the blank line after the source fence; when the
	// fence's own trailing newline is missing (end of file) one more is needed.
	lead := "\n"
	if c.ResultPos.Start > 0 && c.src[c.ResultPos.Start-1] != '\n' {
		lead = "\n\n"
	}
	return Edit{Span: c.ResultPos, Text: lead + t + "\n"}, nil
}

// AssignID returns the edit that writes id into the cell's source fence (§5.1).
//
// The insertion is append-only: every existing entry keeps its text, spacing, and
// order (§9). Assigning an id is the one edit a run makes to a *source* fence
// rather than to results, and it happens at most once in a cell's lifetime.
func (c *Cell) AssignID(id string) (Edit, error) {
	if c.ID != "" {
		return Edit{}, fmt.Errorf("notekit: cell %q already has id %q", c.HeadingText, c.ID)
	}
	if !ValidID(id) {
		return Edit{}, fmt.Errorf("notekit: %q is not a valid cell id (%d lower-case base32 characters)", id, IDLen)
	}
	if c.MetaErr != nil {
		return Edit{}, fmt.Errorf("notekit: cell %q has a malformed info string: %w", c.HeadingText, c.MetaErr)
	}
	info, err := c.Meta.Insert(KeyID, id)
	if err != nil {
		return Edit{}, fmt.Errorf("notekit: cell %q: %w", c.HeadingText, err)
	}
	return Edit{Span: c.InfoSpan, Text: info}, nil
}

// EditProse returns the edit that replaces a prose range.
//
// The caller is responsible for the span lying outside every cell's source fence
// and result position; [Notebook.Apply] rejects overlaps but cannot tell prose from
// anything else.
func (n *Notebook) EditProse(span Span, text string) Edit {
	return Edit{Span: span, Text: text}
}

// SetSource returns the edit that replaces the cell's fence body with text,
// rewriting the whole source fence.
//
// The fence is re-emitted rather than only its body, because fence-length safety
// applies to a source fence as much as to a result (§6): a body containing a run of
// backticks at least as long as the fence would terminate it early and silently turn the
// rest of the cell into prose. Widening the fence is not silent repair — §10 permits a
// tool to rewrite the byte ranges it edited, and the source fence is precisely what is
// being edited here. The info string is preserved exactly, including any `id`.
//
// It fails on an unclosed source fence, for the same reason [Cell.SetResult] does: such
// a fence runs to end of file, and closing it on the author's behalf would be the repair
// §10 forbids. `notefmt` reports the condition so the author can fix it.
func (c *Cell) SetSource(text string) (Edit, error) {
	if !c.Closed {
		return Edit{}, fmt.Errorf(
			"notekit: cell %q has an unclosed source fence; close it before editing the cell",
			c.HeadingText)
	}
	ch := c.fenceCh
	if ch != '`' && ch != '~' {
		ch = '`'
	}

	body := text
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	// A tilde fence's length is bounded by tilde runs, a backtick fence's by backtick
	// runs; measuring the wrong character would leave the fence breakable.
	n := runLen(body, ch) + 1
	if n < 3 {
		n = 3
	}
	fence := strings.Repeat(string(ch), n)

	return Edit{
		Span: c.Source,
		Text: fence + c.Info + "\n" + body + fence + "\n",
	}, nil
}

// runLen returns the longest run of ch in s.
func runLen(s string, ch byte) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	return longest
}
