package serve

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/run"
)

// notebook reads and parses the notebook from disk.
//
// Every handler re-reads rather than caching: a run rewrites the file, so a cached parse
// is stale the moment anything runs. The file is the artifact, so reading it is the
// honest way to know what it says.
func (s *Server) notebook() (*doc.Notebook, []byte, error) {
	src, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil, err
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return nil, nil, err
	}
	return nb, src, nil
}

// handleNotebook renders the whole notebook.
func (s *Server) handleNotebook(c echo.Context) error {
	page, err := s.buildPage()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return s.html(c, http.StatusOK, "page", page)
}

// handleRun submits a cell and returns the polling fragment.
//
// It returns immediately with a run ID rather than waiting (harvest R3); the fragment
// polls [Server.handleRunStatus] until the run reaches a terminal state.
func (s *Server) handleRun(c echo.Context) error {
	cells, err := s.sched.Cells(s.path)
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	index, err := cellIndex(c, cells)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	// Indices are only meaningful against the structure the page was rendered from, and
	// a run writes a result into the cell it names.
	if err := s.checkFingerprint(c); err != nil {
		return err
	}

	id, err := s.sched.Submit(s.path, index)
	if err != nil {
		// Submission is refused for spec reasons — a language mismatch, a full
		// queue — so the message is the useful part.
		return s.flash(c, http.StatusConflict, err.Error())
	}

	r, _ := s.sched.State(id)
	return s.html(c, http.StatusOK, "running", runningView{
		Base:   s.base,
		Index:  index,
		RunID:  string(id),
		State:  r.State.String(),
		PollMS: s.poll.Milliseconds(),
	})
}

// handleRunStatus is what the browser polls.
//
// While the run is pending it returns the spinner fragment again, which re-arms the
// poll. Once terminal it returns the rendered result, and the polling stops because the
// replacement fragment carries no hx-trigger.
func (s *Server) handleRunStatus(c echo.Context) error {
	id := run.ID(c.Param("id"))
	r, ok := s.sched.State(id)
	if !ok {
		return s.flash(c, http.StatusNotFound, "no such run: "+string(id))
	}

	if !r.State.Terminal() {
		return s.html(c, http.StatusOK, "running", runningView{
			Base:   s.base,
			Index:  r.CellIndex,
			RunID:  string(id),
			State:  r.State.String(),
			PollMS: s.poll.Milliseconds(),
		})
	}

	switch r.State {
	case run.Failed:
		// A failed run persisted nothing, so there is no result to show — only the
		// reason. Distinct from a domain failure, which persisted an error block and
		// renders as one.
		msg := "run failed"
		if r.Err != nil {
			msg = r.Err.Error()
		}
		return s.html(c, http.StatusOK, "flash", flashView{Kind: "error", Message: msg})
	case run.Cancelled:
		return s.html(c, http.StatusOK, "flash", flashView{Kind: "info", Message: "run cancelled"})
	}

	nb, src, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()
	if r.CellIndex >= len(cells) {
		return s.flash(c, http.StatusConflict, "the cell no longer exists")
	}
	return s.html(c, http.StatusOK, "result",
		s.buildResult(src, r.CellIndex, cells[r.CellIndex]))
}

// handleProseGet returns a prose region, rendered or as an edit form.
func (s *Server) handleProseGet(c echo.Context) error {
	nb, src, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	ref := c.Param("ref")
	span, err := proseSpan(nb, ref)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	editing := c.QueryParam("edit") != ""
	return s.html(c, http.StatusOK, "prose", s.proseViewFor(src, ref, span, editing, s.canEdit(nb)))
}

// handleProsePut saves a prose edit.
//
// The span is recomputed from a fresh parse rather than taken from the request, so a save
// from a stale page cannot splice into a byte range that now holds something else.
func (s *Server) handleProsePut(c echo.Context) error {
	nb, _, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	ref := c.Param("ref")
	span, err := proseSpan(nb, ref)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}

	text := c.FormValue("text")
	out, err := nb.Apply(nb.EditProse(span, text))
	if err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	// Refuse to save something that no longer parses. This should be unreachable —
	// prose is prose — but a spliced notebook that cannot be read back is the one
	// outcome worth never writing.
	if _, err := doc.Parse(out); err != nil {
		return s.flash(c, http.StatusConflict,
			"that edit would make the notebook unreadable, so it was not saved")
	}
	if err := s.saveAndTell(c, out); err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}

	nb2, src2, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	span2, err := proseSpan(nb2, ref)
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	return s.html(c, http.StatusOK, "prose", s.proseViewFor(src2, ref, span2, false, s.canEdit(nb2)))
}

// save writes the notebook atomically, for the same reason package run does: the file is
// the artifact, so a torn write is unacceptable.
func (s *Server) save(out []byte) error {
	return doc.WriteFileAtomic(s.path, out, 0o644)
}

// saveAndTell saves and then tells the client the structure it now holds, so the next
// request it makes is not rejected as stale.
func (s *Server) saveAndTell(c echo.Context, out []byte) error {
	if err := s.save(out); err != nil {
		return err
	}
	setFingerprint(c, out)
	return nil
}

// handleSidecar serves an artifact from the notebook's sidecar directory.
//
// Only a plain file name is accepted, and it is joined to the sidecar directory after
// stripping any path: a request must not be able to escape into the filesystem.
func (s *Server) handleSidecar(c echo.Context) error {
	name := filepath.Base(c.Param("name"))
	if name == "." || name == ".." || name == "/" {
		return echo.NewHTTPError(http.StatusBadRequest, "bad sidecar name")
	}
	full := filepath.Join(doc.SidecarDir(s.path), name)
	if _, err := os.Stat(full); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "no such sidecar")
	}
	return c.File(full)
}

// flash renders a transient message at the given status.
func (s *Server) flash(c echo.Context, status int, message string) error {
	kind := "error"
	if status < http.StatusBadRequest {
		kind = "info"
	}
	return s.html(c, status, "flash", flashView{Kind: kind, Message: message})
}

// PollIntervalMS exposes the poll cadence, for a tool that wants to display it.
func (s *Server) PollIntervalMS() string { return strconv.FormatInt(s.poll.Milliseconds(), 10) }

// handleRunAll submits every cell the executor claims, in document order.
//
// Runs within a notebook are serialised by the scheduler, so submitting them all at once
// is safe and they execute in order. Cells another executor claims are skipped rather
// than erroring: a real notebook mixes languages.
func (s *Server) handleRunAll(c echo.Context) error {
	cells, err := s.sched.Cells(s.path)
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}

	submitted, skipped := 0, 0
	for i := range cells {
		if _, err := s.sched.Submit(s.path, i); err != nil {
			skipped++
			continue
		}
		submitted++
	}
	if submitted == 0 {
		return s.flash(c, http.StatusOK, "nothing to run")
	}
	// The page reloads so every cell shows its own polling fragment, rather than this
	// handler trying to swap several targets at once.
	c.Response().Header().Set("HX-Refresh", "true")
	msg := fmt.Sprintf("running %d cells", submitted)
	if skipped > 0 {
		msg += fmt.Sprintf(" (%d skipped)", skipped)
	}
	return s.flash(c, http.StatusOK, msg)
}

// handleCancel cancels a run, which for a shell means interrupting the command.
func (s *Server) handleCancel(c echo.Context) error {
	id := run.ID(c.Param("id"))
	if _, ok := s.sched.State(id); !ok {
		return s.flash(c, http.StatusNotFound, "no such run: "+string(id))
	}
	if err := s.sched.Cancel(id); err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	// Report the current state rather than assuming: a run that finished a moment ago
	// is not cancellable, and saying so is better than implying it was.
	r, _ := s.sched.State(id)
	if r.State.Terminal() && r.State != run.Cancelled {
		return s.flash(c, http.StatusOK, "the run already finished")
	}
	return s.flash(c, http.StatusOK, "cancelling…")
}

// handleSourceGet returns a cell, rendered or as a source editor.
func (s *Server) handleSourceGet(c echo.Context) error {
	nb, src, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()
	index, err := cellIndex(c, cells)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	editing := c.QueryParam("edit") != ""
	return s.html(c, http.StatusOK, "cell", s.buildCell(src, index, cells[index], editing, s.canEdit(nb)))
}

// handleSourcePut saves an edited cell source.
//
// The cell is re-resolved from a fresh parse, and the whole source fence is rewritten so
// fence-length safety still holds for the new body (doc.Cell.SetSource). A body
// containing a long backtick run would otherwise terminate its own fence and silently
// turn the rest of the cell into prose.
func (s *Server) handleSourcePut(c echo.Context) error {
	nb, _, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()
	index, err := cellIndex(c, cells)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	// Indices are only meaningful against the structure the page was rendered from, and
	// a source edit replaces the fence it names.
	if err := s.checkFingerprint(c); err != nil {
		return err
	}

	edit, err := cells[index].SetSource(c.FormValue("source"))
	if err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	out, err := nb.Apply(edit)
	if err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}

	// The edit must leave a notebook whose cell is still the same cell. Anything else
	// — a heading swallowed, a fence broken — is not written.
	after, err := doc.Parse(out)
	if err != nil {
		return s.flash(c, http.StatusConflict,
			"that edit would make the notebook unreadable, so it was not saved")
	}
	if len(after.Cells()) != len(cells) {
		return s.flash(c, http.StatusConflict,
			"that edit would change how many cells the notebook has, so it was not saved")
	}
	if err := s.saveAndTell(c, out); err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}

	nb2, src2, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	return s.html(c, http.StatusOK, "cell", s.buildCell(src2, index, nb2.Cells()[index], false, s.canEdit(nb2)))
}

// handleAddCell inserts a new cell (§10 f).
//
// The new cell is a heading plus a source fence and nothing else: a tool that also
// invented prose or a placeholder result would be writing content the user did not ask
// for. `after` names the cell to insert after, or is absent to append at the end.
func (s *Server) handleAddCell(c echo.Context) error {
	nb, _, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()

	lang := c.FormValue("lang")
	if lang == "" {
		lang = s.lang
	}
	spec := doc.NewCell{
		Heading: c.FormValue("heading"),
		Lang:    lang,
		Body:    c.FormValue("body"),
	}
	if lvl := c.FormValue("level"); lvl != "" {
		n, convErr := strconv.Atoi(lvl)
		if convErr != nil {
			return s.flash(c, http.StatusBadRequest, "level must be a number")
		}
		spec.Level = n
	}

	var edit doc.Edit
	switch after := c.FormValue("after"); after {
	case "":
		edit, err = nb.AppendCell(spec)
	default:
		i, convErr := strconv.Atoi(after)
		if convErr != nil || i < 0 || i >= len(cells) {
			return s.flash(c, http.StatusBadRequest, "no such cell to insert after")
		}
		edit, err = nb.InsertCellAfter(cells[i], spec)
	}
	if err != nil {
		// The reasons are spec conditions — a result tag, a bad heading level — so
		// the message is the useful part.
		return s.flash(c, http.StatusBadRequest, err.Error())
	}

	if err := s.applyStructural(nb, len(cells)+1, edit); err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	// The page reloads: cell indices after the insertion point have all shifted, so
	// swapping one fragment would leave the rest addressing the wrong cells.
	c.Response().Header().Set("HX-Refresh", "true")
	return s.flash(c, http.StatusOK, "cell added")
}

// handleDeleteCell removes a cell and everything else in its section (§10 f).
func (s *Server) handleDeleteCell(c echo.Context) error {
	nb, _, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()
	index, err := cellIndex(c, cells)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	// Indices are only meaningful against the structure the page was rendered from, and
	// a delete removes the section it names.
	if err := s.checkFingerprint(c); err != nil {
		return err
	}

	edit, err := nb.DeleteCell(cells[index])
	if err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	if err := s.applyStructural(nb, len(cells)-1, edit); err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return s.flash(c, http.StatusOK, "cell deleted")
}

// handleMoveUp and handleMoveDown reorder a cell (format spec §10 h).
func (s *Server) handleMoveUp(c echo.Context) error   { return s.move(c, true) }
func (s *Server) handleMoveDown(c echo.Context) error { return s.move(c, false) }

// move reorders one cell and refreshes the page, because every index below the moved cell
// has changed.
//
// A no-op says so and writes nothing: §10 h forbids rewriting the file for a move with
// nothing to do, since a spurious change is visible to a reader, a diff and any watcher.
func (s *Server) move(c echo.Context, up bool) error {
	nb, _, err := s.notebook()
	if err != nil {
		return s.flash(c, http.StatusInternalServerError, err.Error())
	}
	cells := nb.Cells()
	index, err := cellIndex(c, cells)
	if err != nil {
		return s.flash(c, http.StatusBadRequest, err.Error())
	}
	// Reordering is the operation that makes a stale index dangerous, so it is the last
	// place to skip this check.
	if err := s.checkFingerprint(c); err != nil {
		return err
	}

	var edits []doc.Edit
	var moved bool
	if up {
		edits, moved, err = nb.MoveCellUp(cells[index])
	} else {
		edits, moved, err = nb.MoveCellDown(cells[index])
	}
	if err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	if !moved {
		where := "last"
		if up {
			where = "first"
		}
		return s.flash(c, http.StatusOK, "that cell is already "+where)
	}
	// The count must not change: a move reorders, it never adds or removes.
	if err := s.applyStructural(nb, len(cells), edits...); err != nil {
		return s.flash(c, http.StatusConflict, err.Error())
	}
	c.Response().Header().Set("HX-Refresh", "true")
	return s.flash(c, http.StatusOK, "cell moved")
}

// applyStructural applies edits that change how many cells the notebook has, and saves
// only if the result parses and the count is what was intended.
//
// The count check is what distinguishes a structural edit from the others: a splice that
// produced a different number of cells than asked for has gone wrong in a way no other
// check would catch, and a notebook is not something to guess with.
func (s *Server) applyStructural(nb *doc.Notebook, wantCells int, edits ...doc.Edit) error {
	out, err := nb.Apply(edits...)
	if err != nil {
		return err
	}
	after, err := doc.Parse(out)
	if err != nil {
		return fmt.Errorf("that edit would make the notebook unreadable, so it was not saved")
	}
	if got := len(after.Cells()); got != wantCells {
		return fmt.Errorf("that edit would leave %d cells rather than %d, so it was not saved",
			got, wantCells)
	}
	// No fingerprint on the response: every caller of this sets HX-Refresh, so the
	// reloaded page carries the new one anyway.
	return s.save(out)
}
