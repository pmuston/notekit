package doc

import (
	"fmt"

	"github.com/pmuston/notekit/meta"
)

// ResultForm is one of the three admissible result constructs (§4.2).
type ResultForm uint8

const (
	ResultOutput  ResultForm = iota // an `output` fence (§6)
	ResultError                     // an `error` fence (§7)
	ResultSidecar                   // provenance comment plus image link (§8)
)

func (f ResultForm) String() string {
	switch f {
	case ResultOutput:
		return "output"
	case ResultError:
		return "error"
	case ResultSidecar:
		return "sidecar"
	}
	return "unknown"
}

// Result is one result construct in a cell's result position.
type Result struct {
	Form ResultForm
	Span Span

	// Meta is the parsed info string for fence forms, or the parsed attribute
	// list for a sidecar provenance comment. Nil when that text was malformed;
	// see MetaErr.
	Meta    *meta.Info
	MetaErr error

	// Dest is the image destination, for ResultSidecar only.
	Dest string
}

// Cell is a heading plus its source fence, and the results paired to it (§4).
type Cell struct {
	// Section spans the heading line through the end of the section — the next
	// ATX heading of any level, or end of file (§4.1).
	Section Span

	Heading     Span   // the heading line, including its newline
	HeadingText string // heading text, closing #'s removed
	Level       int    // 2–6

	// Source spans the whole source fence: opening line, body, closing line.
	Source   Span
	InfoSpan Span // the info string within the opening fence line
	Info     string
	Lang     string // the info string's tag
	Body     Span   // fence body, excluding both fence lines

	// Closed reports whether the source fence is terminated. An unclosed fence
	// runs to end of file, so the cell can be read and run but has nowhere to put
	// a result — see [Cell.SetResult].
	Closed bool

	// Meta is the parsed source-fence info string, or nil when it was malformed.
	// A malformed info string does not stop the fence from being a source fence:
	// §4.3 governs cell detection, §9 governs metadata, and they are independent.
	Meta    *meta.Info
	MetaErr error

	// ID is the cell's durable identity (§5.1), empty when unassigned — the
	// normal state for a cell whose results are inline.
	ID string

	// Slug is recomputed on every parse and carries no identity (§5.2). It may be
	// empty, and two cells may share one.
	Slug string

	// ResultPos is the region a run replaces (§4.2). When the cell has no
	// results it is an empty span positioned just after the source fence, and
	// when it has some it also covers the blank lines separating them from the
	// fence — so replacing it always yields the same shape.
	ResultPos Span
	Results   []Result

	src []byte
}

// HasMetaError reports whether the source fence's info string failed to parse.
// Tools asked to run such a cell must fail loud (§9); tools merely listing cells
// can report it and carry on.
func (c *Cell) HasMetaError() bool { return c.MetaErr != nil }

// SourceText returns the cell's fence body — the code a tool would execute.
func (c *Cell) SourceText() string { return string(c.Body.In(c.src)) }

// buildCells assembles cells from scanned blocks.
func buildCells(src []byte, blocks []block) []*Cell {
	var cells []*Cell

	for i := 0; i < len(blocks); i++ {
		if blocks[i].kind != blkHeading {
			continue
		}
		// §4.1: a section ends at the next ATX heading of any level.
		end := len(blocks)
		for j := i + 1; j < len(blocks); j++ {
			if blocks[j].kind == blkHeading {
				end = j
				break
			}
		}
		if c := buildCell(src, blocks[i:end]); c != nil {
			cells = append(cells, c)
		}
		// Do not skip ahead: the next heading begins its own section, and
		// sections never nest.
	}
	return cells
}

// buildCell builds one cell from a section's blocks, or returns nil when the
// section holds no cell (§4.3).
func buildCell(src []byte, section []block) *Cell {
	head := section[0]
	if head.level < 2 || head.level > 6 {
		return nil
	}

	// The source fence must be the first fence of *any* kind in the section
	// (§4.3), so an untagged or result-tagged first fence means no cell.
	fenceAt := -1
	for i := 1; i < len(section); i++ {
		if section[i].kind == blkFence {
			fenceAt = i
			break
		}
	}
	if fenceAt < 0 {
		return nil
	}
	fence := section[fenceAt]
	switch fence.tag {
	case "", "output", "error":
		return nil
	}

	sectionEnd := section[len(section)-1].span.End
	c := &Cell{
		Section:     Span{Start: head.span.Start, End: sectionEnd},
		Heading:     head.span,
		HeadingText: head.text,
		Level:       head.level,
		Source:      fence.span,
		InfoSpan:    fence.infoSpan,
		Info:        fence.info,
		Lang:        fence.tag,
		Body:        fence.body,
		Closed:      fence.closed,
		Slug:        Slug(head.text),
		src:         src,
	}
	c.Meta, c.MetaErr = meta.Parse(fence.info)
	if c.Meta != nil {
		if e, ok := c.Meta.Get(KeyID); ok && !e.Flag {
			c.ID = e.Value
		}
	}

	c.Results, c.ResultPos = collectResults(src, section[fenceAt+1:], fence.span.End)
	return c
}

// collectResults reads a cell's result position: consecutive result constructs
// after the source fence, blank lines permitted between them (§4.2).
//
// Read is permissive by design — several constructs, and mixed forms, are all
// taken as the cell's results — because a run replaces the whole region, so the
// condition self-heals rather than needing an error.
func collectResults(src []byte, rest []block, fenceEnd int) ([]Result, Span) {
	var results []Result
	// With no results the position is an empty span just past the source fence;
	// with results it starts there too, absorbing the separating blank lines.
	pos := Span{Start: fenceEnd, End: fenceEnd}

	for i := 0; i < len(rest); i++ {
		b := rest[i]
		if b.kind == blkBlank {
			continue
		}

		var r Result
		switch {
		case b.kind == blkFence && (b.tag == "output" || b.tag == "error"):
			r = Result{Span: b.span}
			if b.tag == "output" {
				r.Form = ResultOutput
			} else {
				r.Form = ResultError
			}
			r.Meta, r.MetaErr = meta.Parse(b.info)

		case b.kind == blkComment && i+1 < len(rest) && rest[i+1].kind == blkImage:
			// §8: the comment and the link together are one construct. An image
			// link with no comment before it is always prose, which is what
			// stops a tool overwriting a user's illustration.
			img := rest[i+1]
			r = Result{
				Form: ResultSidecar,
				Span: Span{Start: b.span.Start, End: img.span.End},
				Dest: img.dest,
			}
			// The braces are synthetic: §8 describes the attributes as the §9
			// grammar with them removed. An attribute-less marker must not become
			// `{}`, which §9 rejects as an empty metadata block.
			info := provenanceTag
			if b.attrs != "" {
				info += " {" + b.attrs + "}"
			}
			r.Meta, r.MetaErr = meta.Parse(info)
			i++

		default:
			// Result position ends at the first thing that is neither a result
			// construct nor a blank line.
			return results, pos
		}

		results = append(results, r)
		pos.End = r.Span.End
	}
	return results, pos
}

// provenanceTag is a synthetic tag used only so a provenance comment's attributes
// can be parsed with the §9 grammar, which §8 says they follow "without braces".
const provenanceTag = "result"

// String renders a one-line summary, for notefmt and diagnostics.
func (c *Cell) String() string {
	id := c.ID
	if id == "" {
		id = "-"
	}
	form := "none"
	if len(c.Results) > 0 {
		form = c.Results[0].Form.String()
		if len(c.Results) > 1 {
			form = fmt.Sprintf("%s+%d", form, len(c.Results)-1)
		}
	}
	return fmt.Sprintf("%s\t%s\tid=%s\tslug=%s\tresult=%s", c.HeadingText, c.Lang, id, c.Slug, form)
}

// ProseBefore returns the span of prose between the cell's heading and its source
// fence — the explanatory paragraph a notebook usually carries there.
//
// Editable regions are derived rather than stored: the format has no "prose block"
// construct (§3), so a tool that wants to edit prose computes the span from the
// constructs that *are* modelled. Deriving it also means the span is always current,
// where a stored one would go stale the moment anything above it changed.
func (c *Cell) ProseBefore() Span {
	return Span{Start: c.Heading.End, End: c.Source.Start}
}

// ProseAfter returns the span of prose from the end of the cell's result position to
// the end of its section.
//
// It is empty for a cell whose source fence is unclosed, since such a fence runs to end
// of file (§4.2) and there is nothing after it.
func (c *Cell) ProseAfter() Span {
	if c.ResultPos.End > c.Section.End {
		return Span{Start: c.Section.End, End: c.Section.End}
	}
	return Span{Start: c.ResultPos.End, End: c.Section.End}
}
