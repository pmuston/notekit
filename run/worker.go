package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
)

// worker consumes one notebook's queue in order. One worker per notebook is what
// serialises runs, and running several workers is what lets different notebooks proceed
// concurrently (kit spec §3.4).
func (s *Scheduler) worker(on *openNotebook) {
	defer close(on.done)
	for p := range on.queue {
		s.execute(on, p)
		p.cancel() // release the context now the run is over
		s.mu.Lock()
		delete(s.cancels, p.id)
		s.mu.Unlock()
	}
}

// reload re-reads and re-parses the notebook. Callers must hold on.mu.
//
// Every read of the parse state goes through this, because the scheduler is not the only
// writer: the server edits prose, sources and structure, and a person may have the file
// open in an editor. A cached parse would leave a splice working from byte offsets that no
// longer describe the file — which corrupts it rather than failing, since the offsets are
// still in range. Re-reading costs one file read per run, against executing a command.
func (on *openNotebook) reload() error {
	src, err := os.ReadFile(on.path)
	if err != nil {
		return fmt.Errorf("run: re-reading %s: %w", on.path, err)
	}
	if bytes.Equal(src, on.src) {
		return nil
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return fmt.Errorf("run: %s changed and no longer parses: %w", on.path, err)
	}
	on.nb, on.src = nb, src
	return nil
}

// execute runs one cell and persists its result.
func (s *Scheduler) execute(on *openNotebook, p *pending) {
	if p.ctx.Err() != nil {
		s.update(p.id, func(r *Run) {
			r.State = Cancelled
			r.Finished = s.now()
		})
		return
	}

	s.update(p.id, func(r *Run) {
		r.State = Running
		r.Started = s.now()
	})

	fail := func(err error) {
		s.update(p.id, func(r *Run) {
			r.State = Failed
			r.Err = err
			r.Finished = s.now()
		})
	}

	on.mu.Lock()
	if err := on.reload(); err != nil {
		on.mu.Unlock()
		fail(err)
		return
	}
	cells := on.nb.Cells()
	if p.index >= len(cells) {
		on.mu.Unlock()
		fail(fmt.Errorf("run: cell %d no longer exists", p.index))
		return
	}
	cell := cells[p.index]
	req := exec.Request{
		Source: cell.SourceText(),
		Meta:   cell.Meta,
		Cell: exec.CellRef{
			Index:   p.index,
			Heading: cell.HeadingText,
			Slug:    cell.Slug,
			ID:      cell.ID,
		},
	}
	on.mu.Unlock()

	// A cell whose source fence is unclosed has no result position (§4.2), so the
	// run would have nowhere to put its answer. Refuse before executing rather than
	// after: running a cell and then discarding its result would be worse.
	if !cell.Closed {
		fail(fmt.Errorf("run: cell %q has an unclosed source fence, so its result cannot be persisted", cell.HeadingText))
		return
	}

	result, execErr := on.sess.Execute(p.ctx, req)

	if p.ctx.Err() != nil {
		s.update(p.id, func(r *Run) {
			r.State = Cancelled
			r.Finished = s.now()
		})
		return
	}

	form, truncated, err := s.persist(on, p.index, result, execErr)
	if err != nil {
		fail(err)
		return
	}
	s.update(p.id, func(r *Run) {
		r.State = Done
		r.Form = form
		r.Truncated = truncated
		r.Finished = s.now()
	})
}

// persist writes the run's outcome into the notebook and saves it.
//
// It returns the result form written, whether output was truncated, and an error only
// when the *run* could not complete. A domain failure is not an error here: it is an
// `error` block, which is a successful outcome (format spec §7).
func (s *Scheduler) persist(on *openNotebook, index int, result exec.Result, execErr error) (doc.ResultForm, bool, error) {
	on.mu.Lock()
	defer on.mu.Unlock()

	// The cell may have executed for a while; re-read so the splice is against the
	// file as it is now, not as it was when the run started.
	if err := on.reload(); err != nil {
		return 0, false, err
	}
	cells := on.nb.Cells()
	if index >= len(cells) {
		return 0, false, fmt.Errorf("run: cell %d no longer exists", index)
	}
	cell := cells[index]
	now := s.now()

	// A domain failure persists as a first-class error block, never as degenerate
	// output (format spec §7).
	if execErr != nil {
		var domain *exec.Error
		if !errors.As(execErr, &domain) {
			// Not a domain failure: the session itself broke, so there is no
			// result to record.
			return 0, false, fmt.Errorf("run: executing cell %q: %w", cell.HeadingText, execErr)
		}
		s.setLiveBody(on.path, index, domain.Message)
		body, truncated := doc.Truncate(domain.Message, s.cap)
		block := doc.ResultBlock{
			Form:      doc.ResultError,
			Body:      body,
			Status:    domain.Status,
			Run:       now,
			Tool:      s.tool,
			Truncated: truncated,
		}
		text, err := block.String()
		if err != nil {
			return 0, false, fmt.Errorf("run: building error block: %w", err)
		}
		if err := s.writeInline(on, cell, text); err != nil {
			return 0, false, err
		}
		return doc.ResultError, truncated, nil
	}

	k, ok := s.registry.Lookup(result.Kind)
	if !ok {
		// A kind with no durable form is inadmissible, so an unregistered kind is
		// an error rather than a silent fall back to text.
		return 0, false, fmt.Errorf("run: cell %q returned unregistered kind %q (registered: %v)",
			cell.HeadingText, result.Kind, s.registry.Names())
	}
	durable, err := k.Durable(result.Payload)
	if err != nil {
		return 0, false, fmt.Errorf("run: cell %q: %w", cell.HeadingText, err)
	}
	if err := durable.Validate(); err != nil {
		return 0, false, fmt.Errorf("run: cell %q: %w", cell.HeadingText, err)
	}

	if durable.Inline != nil {
		s.setLiveBody(on.path, index, durable.Inline.Body)
		body, truncated := doc.Truncate(durable.Inline.Body, s.cap)
		// The executor may already have dropped output to bound its own memory, in
		// which case the body is short of the cap and our own check would miss it.
		truncated = truncated || result.Truncated
		block := doc.ResultBlock{
			Form:      doc.ResultOutput,
			Format:    durable.Inline.Format,
			Body:      body,
			Run:       now,
			Tool:      s.tool,
			Truncated: truncated,
		}
		text, err := block.String()
		if err != nil {
			return 0, false, fmt.Errorf("run: building output block: %w", err)
		}
		if err := s.writeInline(on, cell, text); err != nil {
			return 0, false, err
		}
		return doc.ResultOutput, truncated, nil
	}

	if err := s.writeSidecar(on, index, k, durable.Sidecar, now); err != nil {
		return 0, false, err
	}
	return doc.ResultSidecar, false, nil
}

// writeInline splices a result block into the cell's result position and saves.
func (s *Scheduler) writeInline(on *openNotebook, cell *doc.Cell, text string) error {
	edit, err := cell.SetResult(text)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	out, err := on.nb.Apply(edit)
	if err != nil {
		return fmt.Errorf("run: splicing result: %w", err)
	}
	return s.save(on, out)
}

// writeSidecar writes a sidecar kind's artifacts, splices the reference, and saves.
//
// A cell producing a sidecar needs durable identity, so it is assigned an id if it has
// none (§5.1) — the one edit a run makes to a source fence, appended in the same splice
// as the reference so the file is never left half-written.
func (s *Scheduler) writeSidecar(on *openNotebook, index int, k kind.Kind, sc *kind.Sidecar, now interface{ Unix() int64 }) error {
	cells := on.nb.Cells()
	cell := cells[index]

	var edits []doc.Edit
	id := cell.ID
	if id == "" {
		newID, err := s.newID()
		if err != nil {
			return fmt.Errorf("run: %w", err)
		}
		edit, err := cell.AssignID(newID)
		if err != nil {
			return fmt.Errorf("run: %w", err)
		}
		edits = append(edits, edit)
		id = newID
	}

	dir := doc.SidecarDir(on.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("run: creating %s: %w", dir, err)
	}

	kindName := sc.Kind
	if kindName == "" {
		kindName = k.Name
	}

	written := map[string]bool{}
	var primary string
	for _, f := range sc.Files {
		name := doc.SidecarName(cell.Slug, id, f.Ext)
		if err := os.WriteFile(filepath.Join(dir, name), f.Content, 0o644); err != nil {
			return fmt.Errorf("run: writing %s: %w", name, err)
		}
		written[name] = true
		if f.Primary {
			primary = name
		}
	}

	// Results are volatile, and that extends to sidecars: files carrying this
	// cell's id that this run did not write are superseded by it, so they are
	// removed. Orphans — files whose id matches no cell at all — are a different
	// thing entirely and are never touched here (§8.1).
	if err := s.removeSupersededSidecars(dir, id, written); err != nil {
		return err
	}

	alt := sc.Alt
	if alt == "" {
		alt = cell.HeadingText
	}
	ref := doc.SidecarRef{
		Kind: kindName,
		Dest: filepath.ToSlash(filepath.Join(filepath.Base(dir), primary)),
		Alt:  alt,
		Run:  s.now(),
		Tool: s.tool,
	}
	text, err := ref.String()
	if err != nil {
		return fmt.Errorf("run: building sidecar reference: %w", err)
	}
	edit, err := cell.SetResult(text)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	edits = append(edits, edit)

	out, err := on.nb.Apply(edits...)
	if err != nil {
		return fmt.Errorf("run: splicing sidecar reference: %w", err)
	}
	return s.save(on, out)
}

// removeSupersededSidecars deletes files carrying id that this run did not write.
//
// This is the volatile lifecycle applied to sidecars of the cell being run, which the
// user explicitly asked to run — not the silent deletion of user data that §8 forbids.
// The distinction is the id: a file under a *live* cell's id is that cell's own
// previous artifact, superseded by the run that just replaced its result. A file whose
// id matches no cell is an orphan, reported and left alone.
func (s *Scheduler) removeSupersededSidecars(dir, id string, written map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("run: reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || written[e.Name()] {
			continue
		}
		_, fileID, ok := doc.SplitSidecarName(e.Name())
		if !ok || fileID != id {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("run: removing superseded sidecar %s: %w", e.Name(), err)
		}
	}
	return nil
}

// save writes the notebook atomically and refreshes the parse state.
//
// Temp file plus rename, because a crash midway through a direct write would leave a
// truncated notebook — and the file is the artifact.
func (s *Scheduler) save(on *openNotebook, out []byte) error {
	nb, err := doc.Parse(out)
	if err != nil {
		// The splice produced something unparseable, so it is not written. This
		// should be impossible; treating it as fatal rather than saving anyway is
		// what keeps a bug from reaching the user's file.
		return fmt.Errorf("run: spliced notebook no longer parses, refusing to save: %w", err)
	}

	dir := filepath.Dir(on.path)
	tmp, err := os.CreateTemp(dir, ".notekit-*.tmp")
	if err != nil {
		return fmt.Errorf("run: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("run: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("run: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("run: closing temp file: %w", err)
	}

	// Preserve the notebook's existing mode; CreateTemp makes 0600.
	if info, statErr := os.Stat(on.path); statErr == nil {
		if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
			return fmt.Errorf("run: setting mode on temp file: %w", err)
		}
	}
	if err := os.Rename(tmpName, on.path); err != nil {
		return fmt.Errorf("run: renaming temp file over %s: %w", on.path, err)
	}

	on.nb, on.src = nb, out
	return nil
}

// Close destroys a notebook's session after draining its queue.
func (s *Scheduler) Close(ctx context.Context, path string) error {
	path = filepath.Clean(path)

	s.mu.Lock()
	on, ok := s.notebooks[path]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("run: %s is not open", path)
	}
	delete(s.notebooks, path)
	s.mu.Unlock()

	close(on.queue)
	<-on.done
	if err := on.sess.Close(ctx); err != nil {
		return fmt.Errorf("run: closing session for %s: %w", path, err)
	}
	return nil
}

// Shutdown closes every open notebook, destroying every live session.
//
// This is what SIGINT and SIGTERM reach (see [Scheduler.HandleSignals]) and what a
// tool's main must call on a clean exit. Queued runs are drained first, so a run that
// was accepted is not silently dropped. Every session's Close is attempted even if an
// earlier one fails, and the errors are joined — one broken engine must not leave the
// rest running.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	open := make([]*openNotebook, 0, len(s.notebooks))
	for path, on := range s.notebooks {
		open = append(open, on)
		delete(s.notebooks, path)
	}
	s.mu.Unlock()

	var errs []error
	for _, on := range open {
		close(on.queue)
		<-on.done
		if err := on.sess.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("run: closing session for %s: %w", on.path, err))
		}
	}
	return errors.Join(errs...)
}
