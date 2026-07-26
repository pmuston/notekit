// Package exec defines the executor and session contracts — the kit's domain
// boundary.
//
// Interfaces and plain data only, per notekit-kit-spec.md §3.3. An executor declares
// the info-string language tag it claims, owns a session whose lifetime is tied to
// notebook open and close (harvest R1), and returns a typed result kind plus payload —
// or an [*Error] carrying the domain's message and an optional numeric status for the
// error block (format spec §7).
//
// Whether the engine behind a session is embedded (a pty child, an in-process store) or
// external (a network database) never surfaces above this boundary (harvest D5).
//
// Two rules bind every implementation:
//
//   - Honour cancellation. A caller may cancel a run at any time, and the session must
//     remain usable afterwards or report itself unusable by failing subsequent calls.
//   - Never write to the notebook file. Persistence is package run's job, through
//     package doc. An executor that writes to the notebook breaks round-trip identity,
//     because only the splice path knows which bytes it may touch.
package exec

import (
	"context"
	"fmt"

	"github.com/pmuston/notekit/meta"
)

// Executor is a domain's execution engine: the shell, a database driver, a graph
// store. One executor serves every notebook of its language.
type Executor interface {
	// Lang returns the info-string language tag this executor claims, e.g. "sh",
	// "sql", "cypher". A notebook's cells are dispatched by matching this against
	// the source fence's tag.
	Lang() string

	// Open creates a session for one notebook. The path is informational — for
	// resolving relative paths or naming a working directory — and must not be
	// written to.
	Open(ctx context.Context, notebookPath string) (Session, error)
}

// Session is the per-notebook state an executor owns: cwd, environment and shell
// functions for a shell; a graph or connection binding for a database.
//
// One session per notebook, created on open and destroyed on close or process exit
// (harvest R1). Sessions are stateful, so package run never executes two cells of one
// notebook concurrently.
type Session interface {
	// Execute runs one cell to completion. A domain failure — a non-zero exit
	// status, a syntax error from the engine — is reported as an [*Error], not as a
	// zero-value Result: the format has a first-class error block and never folds a
	// failure into degenerate output (format spec §7).
	Execute(ctx context.Context, req Request) (Result, error)

	// Close destroys the session and releases the engine behind it. It must be safe
	// to call once; package run guarantees no Execute is in flight.
	Close(ctx context.Context) error
}

// Request is one cell's execution request.
type Request struct {
	// Source is the cell's fence body, exactly as written.
	Source string

	// Meta is the source fence's parsed info string, delivered uninterpreted: the
	// format assigns no meaning to any key on a source fence (format spec §9), so
	// converting entries into whatever the engine understands is the executor's job.
	// It is nil when the info string was malformed.
	Meta *meta.Info

	// Cell identifies the cell for provenance and diagnostics. Executors must not
	// use it to address the notebook file.
	Cell CellRef
}

// CellRef identifies a cell without exposing the notebook to the executor.
type CellRef struct {
	Index   int    // position in document order
	Heading string // heading text
	Slug    string // cosmetic name (format spec §5.2)
	ID      string // durable identity, empty when the cell has none (§5.1)
}

// Result is a successful execution: a kind name from the rendering contract plus the
// payload that kind's durable writer understands.
//
// The kind must be registered in the tool binary's registry (package kind). An
// unregistered kind is a runtime error, not a silent fallback to text — the two-forms
// rule means a kind with no durable form is inadmissible.
type Result struct {
	Kind    string
	Payload any

	// Truncated reports that the executor bounded its own capture and dropped
	// output. Package run ORs this with its own durable cap, so the `truncated`
	// flag is accurate either way (§6).
	//
	// This exists because an executor reading from a pty or a cursor must bound
	// memory before the runtime ever sees the bytes — and having dropped some, only
	// it knows. Without this field an executor that capped at exactly the durable
	// cap would produce a body the runtime considers complete, silently losing the
	// fact that output was lost.
	Truncated bool
}

// Error is a domain failure, which persists as an `error` block (format spec §7).
//
// Status carries the domain's numeric code where it has one — a shell exit status, a
// database error code — and is nil where it does not. A nil Status is normal, not
// missing information.
type Error struct {
	Message string
	Status  *int
}

func (e *Error) Error() string {
	if e.Status != nil {
		return fmt.Sprintf("%s (status %d)", e.Message, *e.Status)
	}
	return e.Message
}

// NewError builds a domain error with a numeric status.
func NewError(status int, format string, a ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, a...), Status: &status}
}

// NewErrorNoStatus builds a domain error for a domain with no numeric codes.
func NewErrorNoStatus(format string, a ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, a...)}
}
