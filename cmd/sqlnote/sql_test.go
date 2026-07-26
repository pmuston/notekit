package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
	"github.com/pmuston/notekit/meta"
)

func newSession(t *testing.T, nb exec.Notebook, maxRows int) exec.Session {
	t.Helper()
	ex, err := NewExecutor(maxRows)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if nb.Path == "" {
		nb.Path = filepath.Join(t.TempDir(), "notes.md")
	}
	sess, err := ex.Open(context.Background(), nb)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// memSession is a self-contained notebook: no database named, so it runs in memory
// (harvest R2).
func memSession(t *testing.T) exec.Session {
	t.Helper()
	return newSession(t, exec.Notebook{}, DefaultMaxRows)
}

func req(t *testing.T, source, info string) exec.Request {
	t.Helper()
	var m *meta.Info
	if info != "" {
		var err error
		m, err = meta.Parse(info)
		if err != nil {
			t.Fatalf("parsing %q: %v", info, err)
		}
	}
	return exec.Request{Source: source, Meta: m}
}

func runCell(t *testing.T, sess exec.Session, source, info string) (exec.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return sess.Execute(ctx, req(t, source, info))
}

func mustRun(t *testing.T, sess exec.Session, source, info string) exec.Result {
	t.Helper()
	got, err := runCell(t, sess, source, info)
	if err != nil {
		t.Fatalf("Execute(%q): %v", source, err)
	}
	return got
}

func TestLangIsSQL(t *testing.T) {
	ex, err := NewExecutor(DefaultMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Lang() != "sql" {
		t.Errorf("Lang() = %q, want %q", ex.Lang(), "sql")
	}
}

func TestNewExecutorValidation(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := NewExecutor(n); err == nil {
			t.Errorf("NewExecutor(%d) = nil error, want error", n)
		}
	}
}

// TestSelectReturnsATable is the first thing a non-shell domain does differently: results
// arrive structured, so the table kind is reached without the executor formatting anything
// by hand.
func TestSelectReturnsATable(t *testing.T) {
	sess := memSession(t)
	got := mustRun(t, sess, "SELECT 1 AS a, 'x' AS b", "sql")

	if got.Kind != kind.Table {
		t.Fatalf("Kind = %q, want %q", got.Kind, kind.Table)
	}
	p, ok := got.Payload.(kind.TablePayload)
	if !ok {
		t.Fatalf("Payload is %T, want kind.TablePayload", got.Payload)
	}
	// csv by default: it is the more readable of the two on GitHub, where the durable
	// form is what a reader sees.
	if p.Format != kind.CSV {
		t.Errorf("Format = %q, want %q", p.Format, kind.CSV)
	}
	if p.Body != "a,b\n1,x\n" {
		t.Errorf("Body = %q, want %q", p.Body, "a,b\n1,x\n")
	}
}

func TestNonQueryReportsWhatChanged(t *testing.T) {
	sess := memSession(t)
	mustRun(t, sess, "CREATE TABLE t(a INT)", "sql")

	tests := []struct {
		name, source, want string
	}{
		{"create", "CREATE TABLE u(a INT)", "OK\n"},
		{"one row", "INSERT INTO t VALUES (1)", "OK, 1 row affected\n"},
		{"several rows", "INSERT INTO t VALUES (2),(3),(4)", "OK, 3 rows affected\n"},
		{"update", "UPDATE t SET a = a + 1", "OK, 4 rows affected\n"},
		{"delete", "DELETE FROM t", "OK, 4 rows affected\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustRun(t, sess, tt.source, "sql")
			if got.Kind != kind.Text {
				t.Errorf("Kind = %q, want %q", got.Kind, kind.Text)
			}
			if body, _ := got.Payload.(string); body != tt.want {
				t.Errorf("Payload = %q, want %q", body, tt.want)
			}
		})
	}
}

// TestMultiStatementCell covers the shape a real notebook uses: schema and data in one
// cell, with the last statement deciding whether a table comes back.
func TestMultiStatementCell(t *testing.T) {
	sess := memSession(t)

	// No trailing SELECT: reported as text.
	got := mustRun(t, sess, "CREATE TABLE t(a INT);\nINSERT INTO t VALUES (1),(2);", "sql")
	if got.Kind != kind.Text {
		t.Errorf("Kind = %q, want text", got.Kind)
	}

	// Trailing SELECT: every statement runs and the SELECT's rows come back.
	got = mustRun(t, sess, "INSERT INTO t VALUES (3);\nSELECT a FROM t ORDER BY a;", "sql")
	p, _ := got.Payload.(kind.TablePayload)
	if p.Body != "a\n1\n2\n3\n" {
		t.Errorf("Body = %q — the earlier statement did not run, or the SELECT did not see it", p.Body)
	}
}

// TestStatePersistsAcrossCells is harvest R1 for a database, and it is why the session
// holds one dedicated connection: sql.DB is a pool, and for an in-memory database each
// connection is a separate empty database.
func TestStatePersistsAcrossCells(t *testing.T) {
	sess := memSession(t)

	mustRun(t, sess, "CREATE TABLE parts(name TEXT, qty INT)", "sql")
	mustRun(t, sess, "INSERT INTO parts VALUES ('bracket', 120)", "sql")
	// A temp table lives on the connection, so it is the sharpest test of all: it
	// would vanish if a later cell got a different connection.
	mustRun(t, sess, "CREATE TEMP TABLE scratch AS SELECT name FROM parts", "sql")

	got := mustRun(t, sess, "SELECT name FROM scratch", "sql")
	p, _ := got.Payload.(kind.TablePayload)
	if p.Body != "name\nbracket\n" {
		t.Errorf("Body = %q — connection state did not carry between cells", p.Body)
	}

	// A PRAGMA is connection state too.
	mustRun(t, sess, "PRAGMA foreign_keys = ON", "sql")
	got = mustRun(t, sess, "PRAGMA foreign_keys", "sql")
	if p, _ := got.Payload.(kind.TablePayload); !strings.Contains(p.Body, "1") {
		t.Errorf("Body = %q — the PRAGMA did not persist", p.Body)
	}
}

func TestJSONLKeepsNullDistinct(t *testing.T) {
	sess := memSession(t)
	got := mustRun(t, sess, "SELECT 'a' AS s, NULL AS n, 1 AS i, 2.5 AS f", "sql {format=jsonl}")

	p, _ := got.Payload.(kind.TablePayload)
	if p.Format != kind.JSONL {
		t.Fatalf("Format = %q", p.Format)
	}
	// This is jsonl's reason to exist here: csv cannot tell NULL from an empty string.
	for _, want := range []string{`"n":null`, `"s":"a"`, `"i":1`, `"f":2.5`} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("Body %q missing %q", p.Body, want)
		}
	}

	// And csv renders the same NULL as empty, which is the trade-off.
	got = mustRun(t, sess, "SELECT NULL AS n", "sql {format=csv}")
	if p, _ := got.Payload.(kind.TablePayload); p.Body != "n\n\n" {
		t.Errorf("csv Body = %q, want an empty cell", p.Body)
	}
}

func TestUnknownFormatFallsBackToCSV(t *testing.T) {
	// The kit does not transcode, and `table` would reject anything else, so falling
	// back beats failing the run.
	sess := memSession(t)
	got := mustRun(t, sess, "SELECT 1 AS a", "sql {format=tsv}")
	if p, _ := got.Payload.(kind.TablePayload); p.Format != kind.CSV {
		t.Errorf("Format = %q, want %q", p.Format, kind.CSV)
	}
}

func TestValueRendering(t *testing.T) {
	sess := memSession(t)
	got := mustRun(t, sess,
		"SELECT 42 AS i, 'text' AS s, 2.5 AS f, NULL AS n, x'414243' AS blob, 1=1 AS b", "sql")

	p, _ := got.Payload.(kind.TablePayload)
	lines := strings.Split(strings.TrimRight(p.Body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("Body = %q", p.Body)
	}
	// SQLite reports no column type for an expression, so the Go scan type is all
	// there is to go on.
	if lines[1] != "42,text,2.5,,ABC,1" {
		t.Errorf("row = %q, want %q", lines[1], "42,text,2.5,,ABC,1")
	}
}

// TestFloatPrecisionIsExact records a choice that looks like a bug: csv is data other
// tools parse, so the shortest exactly-round-tripping representation is right even when it
// is ugly. Rounding for looks would silently change the value.
func TestFloatPrecisionIsExact(t *testing.T) {
	sess := memSession(t)
	got := mustRun(t, sess, "SELECT 7 * 0.05 AS total", "sql")
	p, _ := got.Payload.(kind.TablePayload)
	if !strings.Contains(p.Body, "0.35000000000000003") {
		t.Errorf("Body = %q, want the exact IEEE-754 value", p.Body)
	}
}

func TestCSVQuotingIsRFC4180(t *testing.T) {
	sess := memSession(t)
	got := mustRun(t, sess,
		`SELECT 'has,comma' AS a, 'has"quote' AS b, 'has`+"\n"+`newline' AS c`, "sql")

	p, _ := got.Payload.(kind.TablePayload)
	// The body must survive a round trip through a csv reader, which is what the
	// rendering contract means by RFC 4180.
	if !strings.Contains(p.Body, `"has,comma"`) {
		t.Errorf("comma not quoted: %q", p.Body)
	}
	if !strings.Contains(p.Body, `"has""quote"`) {
		t.Errorf("quote not doubled: %q", p.Body)
	}
}

// TestSQLErrorIsADomainFailure: the cell ran and the engine said no, so it persists as a
// first-class error block carrying SQLite's result code (§7).
func TestSQLErrorIsADomainFailure(t *testing.T) {
	sess := memSession(t)

	tests := []struct {
		name       string
		setup      []string
		source     string
		wantStatus int
		wantText   string
	}{
		{name: "syntax error", source: "SELCT bogus", wantStatus: 1, wantText: "syntax error"},
		{name: "unknown table", source: "SELECT * FROM nope", wantStatus: 1, wantText: "no such table"},
		{
			name: "unique constraint carries the extended code",
			setup: []string{
				"CREATE TABLE u(a INT PRIMARY KEY)",
				"INSERT INTO u VALUES (1)",
			},
			source:     "INSERT INTO u VALUES (1)",
			wantStatus: 1555,
			wantText:   "UNIQUE constraint failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := memSession(t)
			for _, stmt := range tt.setup {
				mustRun(t, s, stmt, "sql")
			}
			_, err := runCell(t, s, tt.source, "sql")

			var domain *exec.Error
			if !errors.As(err, &domain) {
				t.Fatalf("err = %v (%T), want *exec.Error", err, err)
			}
			if domain.Status == nil || *domain.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %d", domain.Status, tt.wantStatus)
			}
			if !strings.Contains(domain.Message, tt.wantText) {
				t.Errorf("Message = %q, want it to contain %q", domain.Message, tt.wantText)
			}
		})
	}
	_ = sess
}

func TestSessionSurvivesAFailure(t *testing.T) {
	sess := memSession(t)
	if _, err := runCell(t, sess, "SELCT bogus", "sql"); err == nil {
		t.Fatal("expected a domain error")
	}
	got := mustRun(t, sess, "SELECT 1 AS a", "sql")
	if p, _ := got.Payload.(kind.TablePayload); p.Body != "a\n1\n" {
		t.Errorf("the session did not survive: %q", p.Body)
	}
}

// TestRowLimitIsReported covers the kit contract M3 added: the executor bounds its own
// memory and must say so, or the runtime would treat a short result as complete.
func TestRowLimitIsReported(t *testing.T) {
	sess := newSession(t, exec.Notebook{}, 5)
	mustRun(t, sess, "CREATE TABLE t(a INT)", "sql")
	mustRun(t, sess,
		"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 50) "+
			"INSERT INTO t SELECT i FROM n", "sql")

	got := mustRun(t, sess, "SELECT a FROM t ORDER BY a", "sql")
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	p, _ := got.Payload.(kind.TablePayload)
	lines := strings.Split(strings.TrimRight(p.Body, "\n"), "\n")
	if len(lines) != 6 { // header plus five rows
		t.Errorf("got %d lines, want 6: %q", len(lines), p.Body)
	}

	// And the session is still usable: the remaining rows were abandoned cleanly.
	if _, err := runCell(t, sess, "SELECT count(*) AS c FROM t", "sql"); err != nil {
		t.Errorf("the session did not survive truncation: %v", err)
	}
}

func TestEmptyCellIsNotAnError(t *testing.T) {
	sess := memSession(t)
	for _, source := range []string{"", "   ", "\n\n"} {
		got, err := runCell(t, sess, source, "sql")
		if err != nil {
			t.Errorf("Execute(%q) = %v", source, err)
		}
		if got.Kind != kind.Text {
			t.Errorf("Kind = %q, want text", got.Kind)
		}
	}
}

// TestSelfContainedByDefault is harvest R2's first mode: no database named, so the
// notebook builds its own data and a run from empty reconstructs it.
func TestSelfContainedByDefault(t *testing.T) {
	dir := t.TempDir()
	nb := exec.Notebook{Path: filepath.Join(dir, "notes.md")}

	sess := newSession(t, nb, DefaultMaxRows)
	mustRun(t, sess, "CREATE TABLE t(a INT); INSERT INTO t VALUES (1)", "sql")
	if s, ok := sess.(*Session); !ok || !s.InMemory() {
		t.Error("a notebook naming no database should be in memory")
	}
	_ = sess.Close(context.Background())

	// Nothing was written beside the notebook.
	entries, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a self-contained notebook created files: %v", entries)
	}

	// A fresh session starts empty, which is what "run-all from empty reconstructs it"
	// depends on.
	sess2 := newSession(t, nb, DefaultMaxRows)
	if _, err := runCell(t, sess2, "SELECT * FROM t", "sql"); err == nil {
		t.Error("an in-memory database should not survive the session")
	}
}

// TestBoundToAFile is harvest R2's second mode, and checks the path resolves against the
// notebook rather than the process's working directory.
func TestBoundToAFile(t *testing.T) {
	dir := t.TempDir()
	nb := exec.Notebook{
		Path:  filepath.Join(dir, "notes.md"),
		Front: map[string]string{FrontKeyDB: "./data.db"},
	}

	sess := newSession(t, nb, DefaultMaxRows)
	if s, ok := sess.(*Session); !ok || s.InMemory() {
		t.Error("a notebook naming a database should not be in memory")
	}
	mustRun(t, sess, "CREATE TABLE t(a INT); INSERT INTO t VALUES (7)", "sql")
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Resolved beside the notebook, not beside the process.
	if _, err := filepath.Glob(filepath.Join(dir, "data.db")); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "data.db"))
	if len(matches) != 1 {
		t.Fatalf("data.db not created beside the notebook: %v", matches)
	}

	// The data survives, which is what "bound to an external store" means.
	sess2 := newSession(t, nb, DefaultMaxRows)
	got := mustRun(t, sess2, "SELECT a FROM t", "sql")
	if p, _ := got.Payload.(kind.TablePayload); p.Body != "a\n7\n" {
		t.Errorf("Body = %q — the data did not persist", p.Body)
	}
}

func TestFrontMatterKeyIsReadUninterpreted(t *testing.T) {
	// An absolute path is used as given, and an empty value means the same as absent.
	dir := t.TempDir()
	abs := filepath.Join(dir, "abs.db")

	sess := newSession(t, exec.Notebook{
		Path:  filepath.Join(t.TempDir(), "notes.md"),
		Front: map[string]string{FrontKeyDB: abs},
	}, DefaultMaxRows)
	mustRun(t, sess, "CREATE TABLE t(a INT)", "sql")
	_ = sess.Close(context.Background())
	if matches, _ := filepath.Glob(abs); len(matches) != 1 {
		t.Errorf("the absolute path was not used: %v", matches)
	}

	empty := newSession(t, exec.Notebook{
		Path:  filepath.Join(t.TempDir(), "notes.md"),
		Front: map[string]string{FrontKeyDB: ""},
	}, DefaultMaxRows)
	if s, ok := empty.(*Session); !ok || !s.InMemory() {
		t.Error("an empty value should mean the same as an absent key")
	}
}

func TestCancellation(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		sess := memSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := sess.Execute(ctx, req(t, "SELECT 1", "sql")); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("cancelled mid-query, session stays usable", func(t *testing.T) {
		sess := memSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		// A recursive CTE that runs long enough to interrupt.
		_, err := sess.Execute(ctx, req(t,
			"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 20000000) "+
				"SELECT count(*) FROM n", "sql"))
		if err == nil {
			t.Skip("the query finished before cancellation could take effect")
		}
		// The contract requires the session to remain usable, or to report itself
		// unusable. This one remains usable.
		if _, err := runCell(t, sess, "SELECT 1 AS a", "sql"); err != nil {
			t.Errorf("the session did not survive cancellation: %v", err)
		}
	})
}

func TestClosedSessionRefusesWork(t *testing.T) {
	sess := memSession(t)
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent: the scheduler's Shutdown and a tool's defer may both call it.
	if err := sess.Close(context.Background()); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if _, err := sess.Execute(context.Background(), req(t, "SELECT 1", "sql")); err == nil {
		t.Error("Execute on a closed session = nil error, want error")
	}
}

func TestOpenRejectsAnUnusableDatabase(t *testing.T) {
	// A directory is not a database file.
	dir := t.TempDir()
	ex, err := NewExecutor(DefaultMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.Open(context.Background(), exec.Notebook{
		Path:  filepath.Join(dir, "notes.md"),
		Front: map[string]string{FrontKeyDB: "."},
	})
	if err == nil {
		t.Error("Open = nil error, want a refusal for an unusable database")
	}
}

// TestSessionsAreIndependent: one executor serves many notebooks, each with its own
// database — which is why front matter reaches Open rather than configuring the executor.
func TestSessionsAreIndependent(t *testing.T) {
	ex, err := NewExecutor(DefaultMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	open := func(name string) exec.Session {
		s, err := ex.Open(context.Background(), exec.Notebook{
			Path:  filepath.Join(dir, name+".md"),
			Front: map[string]string{FrontKeyDB: "./" + name + ".db"},
		})
		if err != nil {
			t.Fatalf("Open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		return s
	}

	a, b := open("alpha"), open("beta")
	mustRun(t, a, "CREATE TABLE only_in_a(x INT)", "sql")

	// b must not see a's schema.
	if _, err := runCell(t, b, "SELECT * FROM only_in_a", "sql"); err == nil {
		t.Error("two notebooks shared a database")
	}
	if _, err := runCell(t, a, "SELECT * FROM only_in_a", "sql"); err != nil {
		t.Errorf("a lost its own schema: %v", err)
	}
	for _, name := range []string{"alpha.db", "beta.db"} {
		if m, _ := filepath.Glob(filepath.Join(dir, name)); len(m) != 1 {
			t.Errorf("%s was not created", name)
		}
	}
}

func TestDSNReported(t *testing.T) {
	sess := newSession(t, exec.Notebook{
		Path:  filepath.Join(t.TempDir(), "notes.md"),
		Front: map[string]string{FrontKeyDB: "./x.db"},
	}, DefaultMaxRows)
	s, ok := sess.(*Session)
	if !ok {
		t.Fatalf("session is %T", sess)
	}
	if s.DSN() != "./x.db" {
		t.Errorf("DSN() = %q, want the value from front matter", s.DSN())
	}
}

func TestDomainErrorPassesThroughNonSQLiteErrors(t *testing.T) {
	// An error with no result code is a session failure, not a domain one: the run
	// could not complete, so nothing should be persisted.
	plain := fmt.Errorf("connection reset")
	got := domainError(plain)
	var domain *exec.Error
	if errors.As(got, &domain) {
		t.Error("a codeless error must not become a domain failure")
	}
	if domainError(nil) != nil {
		t.Error("domainError(nil) should be nil")
	}
}

// TestRowCountIsNotStickyAcrossCells is a regression for a bug the shipped example
// exposed: SQLite's `changes()` keeps the previous statement's value for anything that is
// not an INSERT, UPDATE or DELETE, so a `CREATE TABLE … AS SELECT`, a bare `SELECT` or a
// `PRAGMA` would report the row count of whatever cell ran before it. A wrong number
// attributed to the wrong statement is worse than no number, so the count is a
// `total_changes()` delta.
func TestRowCountIsNotStickyAcrossCells(t *testing.T) {
	sess := memSession(t)
	mustRun(t, sess, "CREATE TABLE p(n TEXT, q INT)", "sql")

	// Establish a non-zero counter that a sticky read would leak into later cells.
	if got := mustRun(t, sess, "INSERT INTO p VALUES ('a',340),('b',120),('c',45),('d',8)", "sql"); true {
		if body, _ := got.Payload.(string); body != "OK, 4 rows affected\n" {
			t.Fatalf("setup insert reported %q", body)
		}
	}

	tests := []struct{ name, source, want string }{
		// SQLite does not count these as changes, so a delta is zero — which is its
		// own accounting, and far better than inheriting the 4 above.
		{"create table as select", "CREATE TEMP TABLE bulk AS SELECT n,q FROM p WHERE q>=100", "OK\n"},
		{"pragma", "PRAGMA foreign_keys = ON", "OK\n"},
		{"create table", "CREATE TABLE q2(x INT)", "OK\n"},
		// And real changes are still exact.
		{"update one", "UPDATE p SET q = 1 WHERE n = 'd'", "OK, 1 row affected\n"},
		{"delete one", "DELETE FROM p WHERE n = 'd'", "OK, 1 row affected\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustRun(t, sess, tt.source, "sql")
			if body, _ := got.Payload.(string); body != tt.want {
				t.Errorf("Payload = %q, want %q", body, tt.want)
			}
		})
	}

	// The temp table really does hold two rows, so only the count was ever wrong.
	got := mustRun(t, sess, "SELECT count(*) AS c FROM bulk", "sql")
	if p, _ := got.Payload.(kind.TablePayload); p.Body != "c\n2\n" {
		t.Errorf("the temp table holds %q, want 2 rows", p.Body)
	}
}
