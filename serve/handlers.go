package serve

import (
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
	return s.html(c, http.StatusOK, "prose", s.proseViewFor(src, ref, span, editing))
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
	if err := s.save(out); err != nil {
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
	return s.html(c, http.StatusOK, "prose", s.proseViewFor(src2, ref, span2, false))
}

// save writes the notebook atomically, for the same reason package run does: the file is
// the artifact, so a torn write is unacceptable.
func (s *Server) save(out []byte) error {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".notekit-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, statErr := os.Stat(s.path); statErr == nil {
		if err := os.Chmod(name, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(name, s.path)
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
