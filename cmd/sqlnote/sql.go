package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	// Pure Go, no CGO, so a sqlnote binary stays a single static file with no
	// toolchain requirement — which is why the project's driver policy names this one
	// rather than the cgo-based alternative.
	_ "modernc.org/sqlite"

	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
)

// Lang is the info-string tag sqlnote claims.
const Lang = "sql"

// FrontKeyDB is the front-matter key naming the database (§2 passthrough).
const FrontKeyDB = "sqlnote-db"

// memoryDSN is SQLite's in-memory database.
//
// It is the default, and that choice is harvest R2 made concrete: with no database named,
// a notebook is **self-contained** — its cells build the data and a run from empty
// reconstructs it. Name one and the notebook is **bound** to an external store instead.
// R2 called these general properties rather than a Cypher quirk, and they turn out to be
// tool configuration rather than anything the format needs to know.
const memoryDSN = ":memory:"

// Executor runs `sql` cells against SQLite.
//
// All database knowledge lives in this file and in no kit package. The kit sees only an
// [exec.Executor]: whether a session is a pty child or a database handle never surfaces
// above that boundary (harvest D5).
type Executor struct {
	maxRows int
}

// NewExecutor returns a SQL executor. maxRows bounds how many rows one cell may return,
// so a `SELECT * FROM huge` cannot exhaust memory before the runtime sees a byte.
func NewExecutor(maxRows int) (*Executor, error) {
	if maxRows <= 0 {
		return nil, fmt.Errorf("sqlnote: row limit must be positive")
	}
	return &Executor{maxRows: maxRows}, nil
}

func (e *Executor) Lang() string { return Lang }

// Open connects to the notebook's database, one session per notebook (harvest R1).
//
// The database comes from the notebook's own front matter, which is why [exec.Notebook]
// carries it: one executor serves many notebooks, each possibly naming a different
// database, so configuring the executor would not do.
//
// A relative path resolves against the notebook's directory, so `sqlnote-db: ./data.db`
// means what a reader expects wherever the tool was launched from.
func (e *Executor) Open(ctx context.Context, nb exec.Notebook) (exec.Session, error) {
	dsn := nb.FrontValue(FrontKeyDB, memoryDSN)
	resolved := dsn
	if dsn != memoryDSN && !filepath.IsAbs(dsn) && !strings.HasPrefix(dsn, "file:") {
		resolved = filepath.Join(filepath.Dir(nb.Path), dsn)
	}

	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return nil, fmt.Errorf("sqlnote: opening %s: %w", dsn, err)
	}
	// One connection, and only one. sql.DB is a pool, and for an in-memory database
	// every connection is a *separate empty database* — so a pool would silently lose
	// everything between cells. Even on a file, temp tables and PRAGMAs live on the
	// connection. A dedicated conn held for the session's lifetime is what makes
	// harvest R1's "state carries between cells" true here.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlnote: connecting to %s: %w", dsn, err)
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("sqlnote: %s is not usable: %w", dsn, err)
	}

	return &Session{db: db, conn: conn, dsn: dsn, maxRows: e.maxRows}, nil
}

// Session is one notebook's database connection.
type Session struct {
	// mu serialises Execute. The scheduler never runs two cells of one notebook at
	// once, but a session must not rely on a caller's promise for its own safety.
	mu      sync.Mutex
	db      *sql.DB
	conn    *sql.Conn
	dsn     string
	maxRows int
	closed  bool
}

// DSN reports which database this session is bound to, for display.
func (s *Session) DSN() string { return s.dsn }

// InMemory reports whether the notebook is self-contained rather than bound to a file
// (harvest R2).
func (s *Session) InMemory() bool { return s.dsn == memoryDSN }

// Execute runs a cell's SQL.
//
// One Query serves both shapes a cell can take. The driver runs every statement in the
// text and returns the columns of whichever one produced rows; a cell with no final
// SELECT comes back with zero columns, which is how the two are told apart without
// parsing SQL — splitting statements correctly means handling strings, comments and
// nested quoting, and getting it subtly wrong would run the wrong thing.
func (s *Session) Execute(ctx context.Context, req exec.Request) (exec.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return exec.Result{}, errors.New("sqlnote: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return exec.Result{}, err
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		return exec.Result{Kind: kind.Text, Payload: ""}, nil
	}

	// Read the change counter before running, so a non-query cell can report what *it*
	// changed rather than what some earlier cell did — see changedSince.
	before := s.totalChanges(ctx)

	rows, err := s.conn.QueryContext(ctx, source)
	if err != nil {
		return exec.Result{}, domainError(err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return exec.Result{}, domainError(err)
	}

	// No columns: the cell ended in a statement that returns nothing — CREATE,
	// INSERT, PRAGMA. Report what changed rather than an empty table.
	if len(cols) == 0 {
		if err := rows.Close(); err != nil {
			return exec.Result{}, domainError(err)
		}
		return exec.Result{Kind: kind.Text, Payload: s.changedSince(ctx, before)}, nil
	}

	table, truncated, err := s.scan(rows, cols, tableFormat(req))
	if err != nil {
		return exec.Result{}, domainError(err)
	}
	return exec.Result{
		Kind:      kind.Table,
		Payload:   table,
		Truncated: truncated,
	}, nil
}

// tableFormat reads the cell's requested serialisation. csv is the default because it is
// the more readable of the two on GitHub, where the durable form is what a reader sees.
func tableFormat(req exec.Request) string {
	if req.Meta == nil {
		return kind.CSV
	}
	e, ok := req.Meta.Get("format")
	if !ok {
		return kind.CSV
	}
	switch e.Value {
	case kind.JSONL:
		return kind.JSONL
	default:
		// The kit does not transcode, and `table` would reject anything else, so an
		// unrecognised value falls back rather than failing the run.
		return kind.CSV
	}
}

// scan reads rows into the requested serialisation, stopping at the row limit.
//
// The limit is the executor's own memory bound. Having engaged it, only this code knows
// output was dropped, which is what [exec.Result.Truncated] is for.
func (s *Session) scan(rows *sql.Rows, cols []string, format string) (kind.TablePayload, bool, error) {
	var b strings.Builder
	truncated := false

	var w *csv.Writer
	if format == kind.CSV {
		w = csv.NewWriter(&b)
		// RFC 4180 with a header row, which the rendering contract requires of csv.
		if err := w.Write(cols); err != nil {
			return kind.TablePayload{}, false, err
		}
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	n := 0
	for rows.Next() {
		if n >= s.maxRows {
			truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return kind.TablePayload{}, false, err
		}
		n++

		switch format {
		case kind.JSONL:
			obj := make(map[string]any, len(cols))
			for i, c := range cols {
				obj[c] = jsonValue(vals[i])
			}
			line, err := json.Marshal(obj)
			if err != nil {
				return kind.TablePayload{}, false, err
			}
			b.Write(line)
			b.WriteByte('\n')
		default:
			rec := make([]string, len(cols))
			for i := range cols {
				rec[i] = textValue(vals[i])
			}
			if err := w.Write(rec); err != nil {
				return kind.TablePayload{}, false, err
			}
		}
	}
	if w != nil {
		w.Flush()
		if err := w.Error(); err != nil {
			return kind.TablePayload{}, false, err
		}
	}
	// rows.Err reports a failure *during* iteration, which is a different thing from
	// the query failing to start — a mid-stream error would otherwise look like a
	// short result set.
	if err := rows.Err(); err != nil {
		return kind.TablePayload{}, false, err
	}

	return kind.TablePayload{Format: format, Body: b.String()}, truncated, nil
}

// textValue renders a scanned value for a csv cell.
//
// SQLite is dynamically typed and reports no column type for an expression, so the Go
// scan type is the only thing to go on.
func textValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "" // NULL and empty string are indistinguishable in csv; jsonl keeps them apart
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// Shortest representation that round-trips exactly, so `7 * 0.05` renders as
		// 0.35000000000000003 rather than 0.35. That looks like a bug and is not:
		// csv is data other tools parse, and rounding for looks would silently
		// change the value. The sqlite3 CLI prints fewer digits; a notebook's
		// durable form should not.
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// jsonValue renders a scanned value for a jsonl object, keeping NULL distinct from an
// empty string — the reason to choose jsonl over csv.
func jsonValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return t
	}
}

// totalChanges reads SQLite's cumulative row-change counter for this connection.
//
// Per-connection state, which is only meaningful because the session holds one dedicated
// connection: on a pool this would read some other cell's counter, or a fresh zero.
func (s *Session) totalChanges(ctx context.Context) int64 {
	var n int64
	if err := s.conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&n); err != nil {
		return -1 // unknown; changedSince reports plain "OK"
	}
	return n
}

// changedSince reports what a non-query cell changed, as a delta.
//
// A delta rather than `changes()`, because `changes()` is **sticky**: it keeps the previous
// statement's value for anything that is not an INSERT, UPDATE or DELETE. A cell running
// `CREATE TEMP TABLE … AS SELECT`, a bare `SELECT`, or a `PRAGMA` would otherwise report
// the row count of whatever cell ran before it — which is not a rounding error but a wrong
// number attributed to the wrong statement. A `total_changes()` delta is zero for those,
// which is what SQLite's own accounting says, and exact for the rest.
func (s *Session) changedSince(ctx context.Context, before int64) string {
	after := s.totalChanges(ctx)
	if before < 0 || after < 0 {
		return "OK\n"
	}
	switch changed := after - before; changed {
	case 0:
		return "OK\n"
	case 1:
		return "OK, 1 row affected\n"
	default:
		return fmt.Sprintf("OK, %d rows affected\n", changed)
	}
}

// Close releases the connection and the pool behind it.
func (s *Session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	var errs []error
	if err := s.conn.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// sqliteCoder is what modernc.org/sqlite's error type provides. Depending on the
// interface rather than the concrete type keeps the driver's internals out of this.
type sqliteCoder interface {
	Code() int
	Error() string
}

// domainError converts a driver error into the right kind of failure.
//
// A SQL error is a *domain* failure — the cell ran and the engine said no — so it
// persists as a first-class `error` block carrying SQLite's result code as the status
// (§7). Anything without a code is a connection or driver problem, which is a *session*
// failure: the run could not complete, and nothing should be persisted.
func domainError(err error) error {
	if err == nil {
		return nil
	}
	var coder sqliteCoder
	if errors.As(err, &coder) {
		return exec.NewError(coder.Code(), "%s", coder.Error())
	}
	return err
}
