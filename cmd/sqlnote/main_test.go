package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmuston/notekit/doc"
)

const front = "---\nnotekit: 1\ntitle: SQL Notebook\n---\n\n"

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func inDir(t *testing.T, dir string, f func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	f()
}

func TestFindNotebooksAndPicker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.md", front+"## A\n\n```sql\nSELECT 1\n```\n")
	writeFile(t, dir, "plain.md", "# not a notebook\n")

	found, err := findNotebooks(dir)
	if err != nil {
		t.Fatalf("findNotebooks: %v", err)
	}
	if len(found) != 1 || filepath.Base(found[0]) != "one.md" {
		t.Errorf("found = %v", found)
	}

	inDir(t, dir, func() {
		got, err := resolveNotebook("")
		if err != nil {
			t.Fatalf("resolveNotebook: %v", err)
		}
		if filepath.Base(got) != "one.md" {
			t.Errorf("got %q", got)
		}
	})

	// Several candidates are named rather than guessed between.
	writeFile(t, dir, "two.md", front+"## B\n\n```sql\nSELECT 2\n```\n")
	inDir(t, dir, func() {
		if _, err := resolveNotebook(""); err == nil {
			t.Error("want an error naming the candidates")
		}
	})
}

// TestDescribeDB checks the startup line says which mode the notebook is in. Saying "in
// memory" out loud matters: a self-contained notebook discards its data on exit, and a
// user should not discover that afterwards.
func TestDescribeDB(t *testing.T) {
	dir := t.TempDir()
	bound := writeFile(t, dir, "bound.md",
		"---\nnotekit: 1\nsqlnote-db: ./x.db\n---\n\n## A\n\n```sql\nSELECT 1\n```\n")
	self := writeFile(t, dir, "self.md", front+"## A\n\n```sql\nSELECT 1\n```\n")

	if got := describeDB(bound); got != "db ./x.db" {
		t.Errorf("describeDB(bound) = %q", got)
	}
	if got := describeDB(self); !strings.Contains(got, "self-contained") {
		t.Errorf("describeDB(self) = %q", got)
	}
	if got := describeDB(filepath.Join(dir, "nope.md")); got != "unknown database" {
		t.Errorf("describeDB(missing) = %q", got)
	}
	if got := describeDB(writeFile(t, dir, "bad.md", "# no\n")); got != "unknown database" {
		t.Errorf("describeDB(non-notebook) = %q", got)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.md", front+"## A\n\n```sql\nSELECT 1\n```\n")
	tests := []struct {
		name string
		args []string
	}{
		{"two notebooks", []string{path, path}},
		{"unknown flag", []string{"-nope", path}},
		{"missing file", []string{filepath.Join(dir, "nope.md")}},
		{"not a notebook", []string{writeFile(t, dir, "bad.md", "# no\n")}},
		{"zero row limit", []string{"-max-rows", "0", path}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := runMain(tt.args, &out, &errBuf); code != exitUsage {
				t.Errorf("exit = %d, want %d (%s)", code, exitUsage, errBuf.String())
			}
		})
	}
}

func TestListFlag(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", front+"## A\n\n```sql\nSELECT 1\n```\n")
	inDir(t, dir, func() {
		var out, errBuf bytes.Buffer
		if code := runMain([]string{"-list"}, &out, &errBuf); code != exitOK {
			t.Fatalf("exit = %d: %s", code, errBuf.String())
		}
		if !strings.Contains(out.String(), "a.md") {
			t.Errorf("stdout = %q", out.String())
		}
	})
	inDir(t, t.TempDir(), func() {
		var out, errBuf bytes.Buffer
		if code := runMain([]string{"-list"}, &out, &errBuf); code != exitOK {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(out.String(), "no notekit notebooks") {
			t.Errorf("stdout = %q", out.String())
		}
	})
}

// TestEndToEnd is harvest validation gate 4: the whole kit driven by a non-shell domain.
// Nothing in doc, meta, kind, run or serve changed to accommodate SQL beyond the front
// matter the exec contract now carries.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.md",
		"---\nnotekit: 1\ntitle: Parts\nsqlnote-db: ./parts.db\n---\n\n"+
			"Opening prose.\n\n"+
			"## Schema\n\n```sql\nCREATE TABLE IF NOT EXISTS p(name TEXT PRIMARY KEY, qty INT);\nDELETE FROM p;\n```\n\n"+
			"## Load\n\n```sql\nINSERT INTO p VALUES ('a', 1), ('b', 2);\n```\n\n"+
			"## Report\n\n```sql {format=csv}\nSELECT name, qty FROM p ORDER BY name;\n```\n\n"+
			"## As jsonl\n\n```sql {format=jsonl}\nSELECT name, NULL AS note FROM p ORDER BY name;\n```\n\n"+
			"## A failure\n\n```sql\nSELECT * FROM missing;\n```\n")

	addr := freeAddr(t)
	var out, errBuf lockedBuffer
	go func() { runMain([]string{"-addr", addr, path}, &out, &errBuf) }()

	base := "http://" + addr
	waitUp(t, base, &errBuf)
	for i := 0; i < 5; i++ {
		runCellOverHTTP(t, base, i)
	}

	got := readFile(t, path)

	// The table kind is reached for real, with the serialisation the cell asked for.
	if !strings.Contains(got, "```output {format=csv, run=") {
		t.Errorf("csv result missing:\n%s", got)
	}
	if !strings.Contains(got, "name,qty\na,1\nb,2\n") {
		t.Errorf("csv body wrong:\n%s", got)
	}
	if !strings.Contains(got, "```output {format=jsonl, run=") {
		t.Errorf("jsonl result missing:\n%s", got)
	}
	if !strings.Contains(got, `"note":null`) {
		t.Errorf("jsonl should keep NULL distinct:\n%s", got)
	}
	// A SQL error is a first-class error block with the engine's code.
	if !strings.Contains(got, "```error {status=1, run=") {
		t.Errorf("error block missing:\n%s", got)
	}
	if !strings.Contains(got, `tool="sqlnote/1.0"`) {
		t.Errorf("provenance missing:\n%s", got)
	}
	if !strings.Contains(got, "Opening prose.\n") {
		t.Error("prose was lost")
	}

	// The notebook still parses, and the bound database was created beside it.
	if _, err := doc.Parse([]byte(got)); err != nil {
		t.Errorf("the notebook no longer parses: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "parts.db")); len(m) != 1 {
		t.Errorf("the database was not created beside the notebook: %v", m)
	}

	// The UI offers to create `sql` cells, derived from the executor rather than
	// configured — a tool that had to remember would have offered `sh`.
	page := httpGet(t, base+"/")
	if !strings.Contains(page, `value="sql"`) {
		t.Errorf("the add-cell form does not offer sql:\n%s", page)
	}
	// And the csv result renders as a sortable table.
	if !strings.Contains(page, `class="nk-table"`) {
		t.Error("the csv result did not render as a table")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitUp(t *testing.T, base string, errBuf *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v\nstderr: %s", err, errBuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func runCellOverHTTP(t *testing.T, base string, index int) {
	t.Helper()
	resp, err := http.Post(base+"/cells/"+strconv.Itoa(index)+"/run", "", nil)
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST run = %d: %s", resp.StatusCode, body)
	}

	id := ""
	if i := strings.Index(string(body), "/runs/"); i >= 0 {
		rest := string(body)[i+len("/runs/"):]
		if j := strings.IndexAny(rest, `"'`); j > 0 {
			id = rest[:j]
		}
	}
	if id == "" {
		t.Fatalf("no run id in fragment: %s", body)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		b := httpGet(t, base+"/runs/"+id)
		if !strings.Contains(b, "hx-trigger") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cell %d never finished: %s", index, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockedBuffer lets the server goroutine write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- `new` and the engine check ------------------------------------------------------

func TestNewWritesARunnableStarterCell(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parts-list.md")

	if err := createNotebook(path, "sql"); err != nil {
		t.Fatalf("createNotebook: %v", err)
	}

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	nb, err := doc.Parse(src)
	if err != nil {
		t.Fatalf("the notebook it wrote does not parse: %v\n%s", err, src)
	}
	// A title a person would recognise, not the filename.
	if got := nb.Title(); got != "Parts list" {
		t.Errorf("Title() = %q, want %q", got, "Parts list")
	}
	cells := nb.Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want exactly 1", len(cells))
	}
	// The starter cell carries this tool's tag, which is what makes the notebook's engine
	// derivable at all — an empty notebook would have nothing to derive from (§2.1).
	if cells[0].Lang != "sql" {
		t.Errorf("Lang = %q, want %q", cells[0].Lang, "sql")
	}
	if len(cells[0].Results) != 0 {
		t.Error("a new cell must carry no result (§10 f)")
	}
	// And the notebook it just wrote is one this binary agrees to open.
	if err := checkEngine(path, "sql"); err != nil {
		t.Errorf("checkEngine rejected a notebook this tool just created: %v", err)
	}
}

func TestNewRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.md")
	const precious = "notes I would hate to lose\n"
	if err := os.WriteFile(path, []byte(precious), 0o644); err != nil {
		t.Fatal(err)
	}
	// The file is the artifact, so a mistyped path must never destroy one.
	if err := createNotebook(path, "sql"); err == nil {
		t.Fatal("want an error for an existing file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != precious {
		t.Errorf("the existing file was modified: %q", got)
	}
}

func TestCheckEngineRefusesAForeignNotebookAndNamesTheTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foreign.md")
	src := "---\nnotekit: 1\n---\n\n## a\n\n```sh\nls\n```\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkEngine(path, "sql")
	if err == nil {
		t.Fatal("want an error: every cell is sh and this tool runs sql")
	}
	// The message has to point somewhere. Failing per-cell on click with a developer's
	// wording is what this replaces.
	if !strings.Contains(err.Error(), "clinote") {
		t.Errorf("error should name the sibling tool, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sh") {
		t.Errorf("error should name the language found, got: %v", err)
	}
}

func TestCheckEngineAllowsCellLessAndPartialMatches(t *testing.T) {
	dir := t.TempDir()
	// No cells: nothing to contradict, and package run guards each cell anyway. Refusing
	// would block a notebook someone is part-way through writing.
	bare := filepath.Join(dir, "bare.md")
	if err := os.WriteFile(bare, []byte("---\nnotekit: 1\n---\n\njust prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkEngine(bare, "sql"); err != nil {
		t.Errorf("a cell-less notebook must be allowed: %v", err)
	}

	// Mixed: one runnable cell is enough. Refusing the whole file would be stricter than
	// the format, which decides per cell.
	mixed := filepath.Join(dir, "mixed.md")
	src := "---\nnotekit: 1\n---\n\n## a\n\n```sh\nls\n```\n\n## b\n\n```sql\n```\n"
	if err := os.WriteFile(mixed, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkEngine(mixed, "sql"); err != nil {
		t.Errorf("a notebook with one runnable cell must be allowed: %v", err)
	}
}

func TestNewMustComeFirst(t *testing.T) {
	var out, errOut strings.Builder
	// flag stops at the first non-flag argument, so this parses as two paths. The message
	// must say what to do instead of dumping usage.
	code := runMain([]string{"-addr", "127.0.0.1:0", "new", "x.md"}, &out, &errOut)
	if code == exitOK {
		t.Fatal("want a non-zero exit")
	}
	if !strings.Contains(errOut.String(), "must come first") {
		t.Errorf("stderr should explain the ordering, got: %q", errOut.String())
	}
}
