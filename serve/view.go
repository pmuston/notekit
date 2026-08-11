package serve

import (
	"fmt"
	"html/template"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/kind"
)

// pageView is the whole-notebook template's data.
type pageView struct {
	Base     string
	Title    string
	Path     string
	Preamble proseView
	Cells    []cellView
	AddCell  newCellView

	// Fingerprint is the structure this page describes. It goes into the HTML rather than
	// only a header because a full page load is not an HTMX request, so the client has no
	// other way to learn its starting value.
	Fingerprint string

	// Wide requests the full window width instead of a reading column, from the
	// notebook's `width: full` key (§2.2). A presentation hint and nothing more:
	// it picks a CSS class and can grant nothing, which is what makes it safe to
	// read from the notebook itself.
	Wide bool

	// CanEdit is false when the notebook says `editable: false` (§2.3). It hides
	// the affordances; the routes are gated separately, because hiding a button
	// is not a control.
	CanEdit bool

	// LocalFilesUngranted is true when the notebook declared `local-files: true`
	// but the tool did not grant it (§2.4). It is the whole reason that key
	// exists: without it a reader sees a broken image and no explanation.
	LocalFilesUngranted bool
}

// cellView is one cell's template data.
type cellView struct {
	Base    string
	Index   int
	Heading string
	Lang    string
	ID      string
	Source  string

	// Runnable is false when the cell cannot be run, with NotRunnable saying why.
	// The button is disabled rather than hidden, so the reason is discoverable.
	Runnable    bool
	NotRunnable string

	ProseBefore proseView
	ProseAfter  proseView
	Result      resultView

	// Editing switches the source fence for a textarea.
	Editing bool

	// CanEdit is false when the notebook withholds editing (§2.3). Running is
	// never affected.
	CanEdit bool

	// First and Last disable the move buttons at the ends, where a move is a no-op that
	// §10 h says must not write the file.
	First bool
	Last  bool
}

// resultView is a rendered result.
type resultView struct {
	Base      string
	Empty     bool
	HTML      template.HTML
	Meta      string
	Truncated bool

	Sidecar     bool
	SidecarName string
	Alt         string
}

// proseView is one editable prose region.
type proseView struct {
	Base     string
	Ref      string // stable address, e.g. "preamble" or "3-before"
	Text     string
	Editable bool
	Editing  bool
	CanEdit  bool
}

// newCellView is the add-cell form's data.
type newCellView struct {
	Base string
	// Lang is the tag new cells get — the executor's, since a cell tagged anything
	// else could never be run here.
	Lang string
	// Count and Indices drive the position selector, which is omitted entirely when
	// there is nowhere to insert but the end.
	Count   int
	Indices []int
}

// flashView is a transient message.
type flashView struct {
	Kind    string // "info" or "error"
	Message string
}

// runningView is the polling fragment shown while a cell runs.
//
// It carries the run ID so the fragment can offer a Cancel button as well as re-arm the
// poll: interrupting a long command was a v1 feature, and the scheduler has always
// supported it.
type runningView struct {
	Base   string
	Index  int
	RunID  string
	State  string
	PollMS int64
}

// buildPage assembles the whole-notebook view from a fresh parse.
func (s *Server) buildPage() (pageView, error) {
	nb, src, err := s.notebook()
	if err != nil {
		return pageView{}, err
	}

	title := s.title
	if title == "" {
		title = nb.Title()
	}
	if title == "" {
		title = filepath.Base(s.path)
	}

	canEdit := s.canEdit(nb)
	page := pageView{
		Base:        s.base,
		Title:       title,
		Path:        s.path,
		Preamble:    s.proseViewFor(src, "preamble", nb.Preamble(), false, canEdit),
		Fingerprint: fingerprint(nb, src),
		Wide:        s.wide(nb),
		CanEdit:     canEdit,

		LocalFilesUngranted: !s.localFiles && wantsLocalFiles(nb.Front()),
	}
	cells := nb.Cells()
	for i, c := range cells {
		v := s.buildCell(src, i, c, false, canEdit)
		// A disabled button says "this cell cannot move" where a hidden one would leave
		// the user wondering, which is the same reasoning as Runnable above.
		v.First = i == 0
		v.Last = i == len(cells)-1
		page.Cells = append(page.Cells, v)
	}

	page.AddCell = newCellView{Base: s.base, Lang: s.lang, Count: len(page.Cells)}
	for i := range page.Cells {
		page.AddCell.Indices = append(page.AddCell.Indices, i)
	}
	return page, nil
}

// buildCell assembles one cell's view.
func (s *Server) buildCell(src []byte, i int, c *doc.Cell, editing, canEdit bool) cellView {
	v := cellView{
		Base:        s.base,
		Index:       i,
		Heading:     c.HeadingText,
		Lang:        c.Lang,
		ID:          c.ID,
		Source:      c.SourceText(),
		Runnable:    true,
		ProseBefore: s.proseViewFor(src, strconv.Itoa(i)+"-before", c.ProseBefore(), false, canEdit),
		ProseAfter:  s.proseViewFor(src, strconv.Itoa(i)+"-after", c.ProseAfter(), false, canEdit),
		Result:      s.buildResult(src, i, c),
		Editing:     editing,
		CanEdit:     canEdit,
	}

	// The reasons a cell cannot be run are spec conditions, not UI preferences, so
	// each names the rule.
	switch {
	case !c.Closed:
		v.Runnable, v.NotRunnable = false,
			"the source fence is unclosed, so a result cannot be persisted"
	case c.MetaErr != nil:
		v.Runnable, v.NotRunnable = false,
			"the info string is malformed: "+c.MetaErr.Error()
	}
	return v
}

// buildResult renders a cell's persisted result, preferring this process's pre-strip
// output so colour appears live.
func (s *Server) buildResult(src []byte, index int, c *doc.Cell) resultView {
	if len(c.Results) == 0 {
		return resultView{Base: s.base, Empty: true}
	}
	// Read tolerates several constructs (§4.2); the UI shows the first, because the
	// next run collapses them to one anyway.
	r := c.Results[0]

	switch r.Form {
	case doc.ResultSidecar:
		return resultView{
			Base:        s.base,
			Sidecar:     true,
			SidecarName: filepath.Base(r.Dest),
			Alt:         c.HeadingText,
			Meta:        metaSummary(r),
		}

	case doc.ResultError:
		body, truncated := s.resultBody(src, index, r)
		return resultView{
			Base:      s.base,
			HTML:      kind.LiveError(body),
			Meta:      metaSummary(r),
			Truncated: truncated,
		}

	default:
		body, truncated := s.resultBody(src, index, r)
		format := ""
		if r.Meta != nil {
			if e, ok := r.Meta.Get("format"); ok {
				format = e.Value
			}
		}
		html, err := s.renderLive(format, body, truncated)
		if err != nil {
			// An unrenderable result is shown as plain text rather than as an
			// error: the body is what the run produced, and hiding it helps nobody.
			html = template.HTML(`<pre class="nk-output nk-malformed">` +
				template.HTMLEscapeString(body) + `</pre>`)
		}
		return resultView{
			Base:      s.base,
			HTML:      html,
			Meta:      metaSummary(r),
			Truncated: truncated,
		}
	}
}

// renderLive dispatches to the kind registry's live half by durable `format` value.
func (s *Server) renderLive(format, body string, truncated bool) (template.HTML, error) {
	k, ok := s.registry.LookupFormat(format)
	if !ok || k.Live == nil {
		return "", fmt.Errorf("serve: no live renderer for format %q", format)
	}
	return k.Live(kind.LiveInput{Format: format, Body: body, Truncated: truncated})
}

// resultBody returns the body to render and whether it was truncated.
//
// The live body — this process's pre-strip output — is preferred when present, which is
// the only way colour ever reaches the browser: ANSI is stripped before persisting (§6),
// so a body read from disk has none. Falling back to the durable body is what makes a
// reloaded notebook render at all.
func (s *Server) resultBody(src []byte, index int, r doc.Result) (string, bool) {
	truncated := false
	if r.Meta != nil {
		if e, ok := r.Meta.Get("truncated"); ok && e.Flag {
			truncated = true
		}
	}
	if live, ok := s.sched.LiveBody(s.path, index); ok {
		return live, truncated
	}
	return fenceBody(src, r), truncated
}

// fenceBody extracts a result fence's body from the notebook bytes.
//
// The result's span covers the whole construct including both fence lines, so the body
// is what lies between them. Doing it by line rather than re-parsing keeps this cheap
// and avoids a second source of truth about where a fence body starts.
func fenceBody(src []byte, r doc.Result) string {
	text := string(r.Span.In(src))
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return ""
	}
	// Drop the opening fence line and any trailing closing fence line.
	body := lines[1:]
	for len(body) > 0 {
		last := strings.TrimRight(body[len(body)-1], " \t")
		if last == "" || isFenceLine(last) {
			body = body[:len(body)-1]
			continue
		}
		break
	}
	if len(body) == 0 {
		return ""
	}
	return strings.Join(body, "\n") + "\n"
}

func isFenceLine(line string) bool {
	t := strings.TrimLeft(line, " ")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// metaSummary renders a result's provenance for display: when it ran, and by what.
func metaSummary(r doc.Result) string {
	if r.Meta == nil {
		return ""
	}
	var parts []string
	if e, ok := r.Meta.Get("run"); ok {
		parts = append(parts, e.Value)
	}
	if e, ok := r.Meta.Get("tool"); ok {
		parts = append(parts, e.Value)
	}
	if e, ok := r.Meta.Get("status"); ok {
		parts = append(parts, "status "+e.Value)
	}
	return strings.Join(parts, " · ")
}

// proseViewFor builds one prose region's view.
func (s *Server) proseViewFor(src []byte, ref string, span doc.Span, editing, canEdit bool) proseView {
	return proseView{
		Base:     s.base,
		Ref:      ref,
		Text:     string(span.In(src)),
		Editable: true,
		Editing:  editing,
		CanEdit:  canEdit,
	}
}

// proseSpan resolves a prose reference against a fresh parse.
//
// References are symbolic — "preamble", "2-before", "2-after" — rather than byte offsets
// supplied by the client. That matters: a byte range from a stale page would splice into
// whatever now occupies those bytes, and results move on every run. Recomputing from the
// current parse means a stale reference addresses the wrong *region* at worst, never the
// middle of a fence.
func proseSpan(nb *doc.Notebook, ref string) (doc.Span, error) {
	if ref == "preamble" {
		return nb.Preamble(), nil
	}
	i := strings.LastIndex(ref, "-")
	if i < 0 {
		return doc.Span{}, fmt.Errorf("serve: %q is not a prose reference", ref)
	}
	index, err := strconv.Atoi(ref[:i])
	if err != nil {
		return doc.Span{}, fmt.Errorf("serve: %q is not a prose reference", ref)
	}
	cells := nb.Cells()
	if index < 0 || index >= len(cells) {
		return doc.Span{}, fmt.Errorf("serve: cell %d does not exist", index)
	}
	switch ref[i+1:] {
	case "before":
		return cells[index].ProseBefore(), nil
	case "after":
		return cells[index].ProseAfter(), nil
	}
	return doc.Span{}, fmt.Errorf("serve: %q is not a prose reference", ref)
}

// cellIndex reads and validates a :index path parameter.
func cellIndex(c echo.Context, cells []*doc.Cell) (int, error) {
	i, err := strconv.Atoi(c.Param("index"))
	if err != nil {
		return 0, fmt.Errorf("serve: %q is not a cell index", c.Param("index"))
	}
	if i < 0 || i >= len(cells) {
		return 0, fmt.Errorf("serve: cell %d does not exist", i)
	}
	return i, nil
}
