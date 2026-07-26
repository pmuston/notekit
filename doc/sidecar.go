package doc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SidecarDir returns the sidecar directory name for a notebook path:
// `<notebook-stem>.assets` beside the file (§8).
func SidecarDir(notebookPath string) string {
	base := filepath.Base(notebookPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(filepath.Dir(notebookPath), stem+".assets")
}

// SidecarState is what a sidecar file's name implies about the notebook.
type SidecarState uint8

const (
	// SidecarCurrent: the file belongs to a live cell and is already named for
	// that cell's slug. Nothing to do.
	SidecarCurrent SidecarState = iota

	// SidecarStale: the file belongs to a live cell but carries an older slug,
	// because the heading was renamed. The fix is to rename the file to Want —
	// the id matched, so the artifact is not orphaned (§8.1).
	SidecarStale

	// SidecarOrphan: the file's id matches no cell in the notebook, which now
	// means exactly one thing — the cell was deleted. Report, never delete (§8).
	SidecarOrphan

	// SidecarForeign: the name carries no valid id, so notekit did not write it
	// and it is none of notekit's business.
	SidecarForeign
)

func (s SidecarState) String() string {
	switch s {
	case SidecarCurrent:
		return "current"
	case SidecarStale:
		return "stale"
	case SidecarOrphan:
		return "orphan"
	case SidecarForeign:
		return "foreign"
	}
	return "unknown"
}

// Sidecar is the classification of one file in the sidecar directory.
type Sidecar struct {
	Name  string // the file name as found
	Want  string // the name it should have; empty unless State is SidecarStale
	ID    string // the id encoded in the name, empty when SidecarForeign
	Slug  string // the slug encoded in the name
	State SidecarState
	Cell  *Cell // the owning cell, nil unless Current or Stale
}

// ClassifySidecars matches a directory listing against a notebook's cells (§8.1).
//
// It is deliberately a pure function over file *names*: the caller supplies the
// listing and performs any rename, so this package needs no filesystem access and the
// classification is trivially testable. Nothing here deletes anything — an orphan is
// reported and left in place.
//
// Because identity is the stored id and never the slug, a renamed heading produces
// SidecarStale (rename the file) rather than an orphan, and reordering cells or
// inserting a cell with a duplicate heading produces no change at all.
func ClassifySidecars(names []string, cells []*Cell) []Sidecar {
	byID := make(map[string]*Cell, len(cells))
	for _, c := range cells {
		if c.ID != "" {
			byID[c.ID] = c
		}
	}

	out := make([]Sidecar, 0, len(names))
	for _, name := range names {
		slug, id, ok := SplitSidecarName(name)
		if !ok {
			out = append(out, Sidecar{Name: name, State: SidecarForeign})
			continue
		}
		s := Sidecar{Name: name, ID: id, Slug: slug}
		c, found := byID[id]
		if !found {
			s.State = SidecarOrphan
			out = append(out, s)
			continue
		}
		s.Cell = c
		if slug == c.Slug {
			s.State = SidecarCurrent
		} else {
			s.State = SidecarStale
			s.Want = SidecarName(c.Slug, id, filepath.Ext(name))
		}
		out = append(out, s)
	}
	return out
}

// WriteFileAtomic writes data to path via a temp file in the same directory, then
// renames it into place.
//
// The file is the artifact, so a torn write is unacceptable — and this applies to sidecar
// artifacts as much as to the notebook (§8). A reader must never see a half-written PNG,
// which for a browser fetching an image mid-run is otherwise exactly what happens. Verified
// against priortool, which reached the same conclusion for the same reason.
//
// An existing file's mode is preserved; otherwise perm applies. The temp file is removed
// on any failure, so a failed write leaves nothing behind.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".notekit-*.tmp")
	if err != nil {
		return fmt.Errorf("notekit: creating a temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("notekit: writing %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("notekit: syncing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("notekit: closing %s: %w", name, err)
	}

	// CreateTemp makes 0600, so the mode has to be set explicitly either way.
	mode := perm
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("notekit: setting the mode on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("notekit: renaming %s over %s: %w", name, path, err)
	}
	return nil
}
