package doc

import (
	"fmt"
	"strings"
)

// MoveCellUp returns the edits that exchange c with the cell above it, and reports whether
// there was anything to do (§10 h).
//
// ok is false, with no error, when c is already the first cell — a no-op must produce no
// edits at all, so a tool cannot rewrite the file for nothing.
func (n *Notebook) MoveCellUp(c *Cell) ([]Edit, bool, error) {
	i, err := n.indexOf(c)
	if err != nil {
		return nil, false, err
	}
	if i == 0 {
		return nil, false, nil
	}
	edits, err := n.trySwap(n.cells[i-1], n.cells[i])
	if err != nil {
		return nil, false, err
	}
	return edits, true, nil
}

// MoveCellDown returns the edits that exchange c with the cell below it, and reports
// whether there was anything to do (§10 h).
//
// ok is false, with no error, when c is already the last cell.
func (n *Notebook) MoveCellDown(c *Cell) ([]Edit, bool, error) {
	i, err := n.indexOf(c)
	if err != nil {
		return nil, false, err
	}
	if i == len(n.cells)-1 {
		return nil, false, nil
	}
	edits, err := n.trySwap(n.cells[i], n.cells[i+1])
	if err != nil {
		return nil, false, err
	}
	return edits, true, nil
}

// trySwap builds the swap and then checks it, returning edits only if applying them would
// leave the notebook with the same cells in the expected order.
//
// Verifying beats enumerating. A section is not guaranteed to be self-contained: an unclosed
// fence anywhere inside one runs to end of file, so relocating that section can turn every
// following heading into its body. The obvious case is an unclosed *source* fence, which
// [movable] names precisely because a clear message is worth having — but a fuzzer found a
// second shape within minutes, an unclosed fence *after* a closed source fence, and there is
// no reason to believe that is the last. A move is a deliberate user action rather than a hot
// path, so it can afford to confirm its own work instead of trusting an argument about which
// cases exist.
func (n *Notebook) trySwap(upper, lower *Cell) ([]Edit, error) {
	if err := movable(upper, lower); err != nil {
		return nil, err
	}
	edits := n.swapSections(upper, lower)

	out, err := n.Apply(edits...)
	if err != nil {
		return nil, err
	}
	after, err := Parse(out)
	if err != nil {
		return nil, fmt.Errorf("notekit: moving cell %q would not leave a readable "+
			"notebook, so it was refused: %w", upper.HeadingText, err)
	}
	if err := sameCells(n.cells, after.cells); err != nil {
		return nil, fmt.Errorf("notekit: moving cell %q would change the notebook's "+
			"structure, so it was refused: %w — a fence inside one of the two sections is "+
			"probably unclosed, which makes it run to end of file",
			upper.HeadingText, err)
	}
	return edits, nil
}

// sameCells reports whether two parses hold the same cells, ignoring order.
//
// Comparing source-fence bytes rather than headings: headings may be empty or duplicated
// (§5.2), so they do not identify a cell, while the fence a cell was built from does.
//
// Trailing whitespace is trimmed before comparing, because a fence that used to sit at end
// of file legitimately gains the newline it needs once something follows it — see endLine.
func sameCells(before, after []*Cell) error {
	if len(before) != len(after) {
		return fmt.Errorf("%d cells became %d", len(before), len(after))
	}
	count := map[string]int{}
	for _, c := range before {
		count[strings.TrimRight(string(c.Source.In(c.src)), " \t\n")]++
	}
	for _, c := range after {
		count[strings.TrimRight(string(c.Source.In(c.src)), " \t\n")]--
	}
	for _, n := range count {
		if n != 0 {
			return fmt.Errorf("the set of source fences changed")
		}
	}
	return nil
}

// movable refuses a swap that would corrupt the document.
//
// An unclosed source fence runs to end of file (§4.2), so it is only harmless while its cell
// is last. Relocating it to any earlier position turns every following heading and fence
// into its body — a fuzzer found exactly that, collapsing two cells into one — and moving
// another cell past it does the same thing, since a swap relocates both. Either participant
// being unclosed is therefore a refusal.
//
// Closing the fence first would be the silent repair §10 forbids, and is why this returns an
// error rather than fixing anything. [Cell.SetResult] refuses the same construct for the
// same reason.
func movable(upper, lower *Cell) error {
	for _, c := range []*Cell{upper, lower} {
		if !c.Closed {
			return fmt.Errorf("notekit: cell %q has an unclosed source fence, which runs "+
				"to end of file, so moving it would swallow whatever followed; close the "+
				"fence first", c.HeadingText)
		}
	}
	return nil
}

// indexOf locates c among the notebook's cells.
//
// Identity is by pointer: a Cell carries byte spans into one particular parse, so a cell
// from a different parse of the same file would splice against offsets that need not still
// describe it. Refusing is the only safe answer.
func (n *Notebook) indexOf(c *Cell) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("notekit: moving a cell needs a cell")
	}
	for i, cell := range n.cells {
		if cell == c {
			return i, nil
		}
	}
	return 0, fmt.Errorf("notekit: the cell does not belong to this notebook")
}

// swapSections exchanges the *contents* of two cells' section slots, leaving each slot's
// trailing blank lines where they are.
//
// That split is the whole design. A blank line separating two sections falls inside the
// earlier section's span (§4.1, §10.1), so moving spans wholesale would drag separators
// around and change the document's shape — and, where the later section runs to end of
// file, could leave a heading with no newline before it, which stops it being a heading at
// all. Treating the separator as belonging to the *slot* rather than to the section keeps
// the shape fixed and makes a move exactly symmetric: moving a cell away and back restores
// the file byte for byte.
//
// The two cells need not be adjacent in bytes. A heading whose section holds no cell (§4.3)
// can sit between them, and it stays where it is — only the two cells trade places.
func (n *Notebook) swapSections(upper, lower *Cell) []Edit {
	contentUpper, trailUpper := splitSection(n.src, upper.Section)
	contentLower, trailLower := splitSection(n.src, lower.Section)

	// endLine before appending the slot's separator, or the separator gets absorbed as the
	// content's own line terminator: content taken from the end of the file has no trailing
	// newline, so `...```" + "\n"` yields one line ending rather than a blank line, and the
	// seam silently loses its blank line. That asymmetry is what made a move irreversible
	// until it was fixed.
	upperText := endLine(contentLower) + trailUpper
	lowerText := endLine(contentUpper) + trailLower

	// The file's own ending is not a seam and is preserved exactly, including the absence
	// of a final newline.
	if lower.Section.End == len(n.src) {
		lowerText = matchFinalNewline(lowerText, n.src)
	}

	return []Edit{
		{Span: upper.Section, Text: upperText},
		{Span: lower.Section, Text: lowerText},
	}
}

// endLine gives s a trailing newline if it lacks one, so that whatever follows begins a
// line of its own. A heading that does not begin a line is not a heading.
func endLine(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// splitSection separates a section span into its content and the blank lines trailing it.
//
// The trailing blank lines are the separator to whatever comes next. They belong to the
// position, not to the cell — see [Notebook.swapSections].
func splitSection(src []byte, sp Span) (content, trailing string) {
	s := string(sp.In(src))
	lines := splitAfterNewline(s)
	k := len(lines)
	for k > 0 && strings.TrimSpace(lines[k-1]) == "" {
		k--
	}
	return strings.Join(lines[:k], ""), strings.Join(lines[k:], "")
}

// splitAfterNewline splits s after each newline, keeping the newlines.
func splitAfterNewline(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// matchFinalNewline makes text end the way src does, so a file that ended without a
// trailing newline still does.
func matchFinalNewline(text string, src []byte) string {
	srcEndsNL := len(src) > 0 && src[len(src)-1] == '\n'
	textEndsNL := strings.HasSuffix(text, "\n")
	switch {
	case srcEndsNL && !textEndsNL:
		return text + "\n"
	case !srcEndsNL && textEndsNL:
		return strings.TrimSuffix(text, "\n")
	}
	return text
}
