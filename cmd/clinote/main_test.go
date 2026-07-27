//go:build unix

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
	"github.com/pmuston/notekit/internal/siblings"
	"github.com/pmuston/notekit/notetool"
)

const front = "---\nnotekit: 1\ntitle: Shell Notebook\n---\n\n"

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// inDir runs f with the process working directory set to dir, which is how the picker's
// "current directory" behaviour gets exercised.
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

func TestDefaultShell(t *testing.T) {
	old := os.Getenv("SHELL")
	defer os.Setenv("SHELL", old)

	tests := []struct{ shell, want string }{
		{"/bin/bash", "bash"},
		{"/usr/local/bin/zsh", "zsh"},
		{"/bin/fish", "bash"}, // unsupported falls back rather than failing later
		{"", "bash"},
	}
	for _, tt := range tests {
		os.Setenv("SHELL", tt.shell)
		if got := defaultShell(); got != tt.want {
			t.Errorf("SHELL=%q: defaultShell() = %q, want %q", tt.shell, got, tt.want)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.md", front+"## A\n\n```sh\necho x\n```\n")

	tests := []struct {
		name string
		args []string
	}{
		{"two notebooks", []string{path, path}},
		{"unknown flag", []string{"-nope", path}},
		{"missing file", []string{filepath.Join(dir, "nope.md")}},
		{"not a notebook", []string{writeFile(t, dir, "bad.md", "# no\n")}},
		{"unsupported shell", []string{"-shell", "fish", path}},
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
	writeFile(t, dir, "a.md", front+"## A\n\n```sh\nx\n```\n")
	writeFile(t, dir, "plain.md", "# not a notebook\n")

	inDir(t, dir, func() {
		var out, errBuf bytes.Buffer
		if code := runMain([]string{"-list"}, &out, &errBuf); code != exitOK {
			t.Fatalf("exit = %d: %s", code, errBuf.String())
		}
		if !strings.Contains(out.String(), "a.md") {
			t.Errorf("stdout = %q", out.String())
		}
		if strings.Contains(out.String(), "plain.md") {
			t.Errorf("a non-notebook was listed: %q", out.String())
		}
	})
}

func TestListFlagWithNoNotebooks(t *testing.T) {
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

// TestEndToEnd is the M3 gate: the real binary, a real shell, the whole kit. It runs
// cells over HTTP and checks what landed on disk.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.md", front+
		"Opening prose.\n\n"+
		"## Set state\n\n```sh\nNOTEKIT_E2E=carried\n```\n\n"+
		"## Read state\n\n```sh\necho \"$NOTEKIT_E2E\"\n```\n\n"+
		"## A table\n\n```sh {format=csv}\nprintf 'a,b\\n1,2\\n'\n```\n\n"+
		"## A failure\n\n```sh\nexit_probe() { return 3; }\nexit_probe\n```\n")

	addr := freeAddr(t)
	var out, errBuf lockedBuffer
	go func() { runMain([]string{"-addr", addr, "-shell", testShell, path}, &out, &errBuf) }()

	base := "http://" + addr
	waitUp(t, base, &errBuf)

	for i := 0; i < 4; i++ {
		runCellOverHTTP(t, base, i)
	}

	got := readFile(t, path)

	// State carried between cells through one shell (harvest R1).
	if !strings.Contains(got, "```output {run=") || !strings.Contains(got, "carried") {
		t.Errorf("state did not carry between cells:\n%s", got)
	}
	// The table kept its serialisation.
	if !strings.Contains(got, "```output {format=csv, run=") {
		t.Errorf("csv format missing:\n%s", got)
	}
	// The failure is a first-class error block with the domain's status.
	if !strings.Contains(got, "```error {status=3, run=") {
		t.Errorf("error block missing:\n%s", got)
	}
	// Provenance names this tool and version.
	if !strings.Contains(got, `tool="clinote/2.0"`) {
		t.Errorf("provenance missing:\n%s", got)
	}
	// Prose is untouched.
	if !strings.Contains(got, "Opening prose.\n") {
		t.Errorf("prose was lost:\n%s", got)
	}
	// And no sentinel leaked into the notebook.
	if strings.Contains(got, "__NOTEKIT_END_") {
		t.Errorf("the sentinel leaked into the notebook:\n%s", got)
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

// runCellOverHTTP posts a run and polls until the fragment stops re-arming, which is
// exactly what the browser does.
func runCellOverHTTP(t *testing.T, base string, index int) {
	t.Helper()
	resp, err := http.Post(base+"/cells/"+itoa(index)+"/run", "", nil)
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
		r, err := http.Get(base + "/runs/" + id)
		if err != nil {
			t.Fatalf("GET run status: %v", err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !strings.Contains(string(b), "hx-trigger") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cell %d never finished: %s", index, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

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

	if err := testTool().Create(path, starterCell("sh")); err != nil {
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
	if cells[0].Lang != "sh" {
		t.Errorf("Lang = %q, want %q", cells[0].Lang, "sh")
	}
	if len(cells[0].Results) != 0 {
		t.Error("a new cell must carry no result (§10 f)")
	}
	// And the notebook it just wrote is one this binary agrees to open.
	if err := testTool().CheckEngine(path); err != nil {
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
	if err := testTool().Create(path, starterCell("sh")); err == nil {
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
	src := "---\nnotekit: 1\n---\n\n## a\n\n```sql\nSELECT 1;\n```\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := testTool().CheckEngine(path)
	if err == nil {
		t.Fatal("want an error: every cell is sql and this tool runs sh")
	}
	// The message has to point somewhere. Failing per-cell on click with a developer's
	// wording is what this replaces.
	if !strings.Contains(err.Error(), "sqlnote") {
		t.Errorf("error should name the sibling tool, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sql") {
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
	if err := testTool().CheckEngine(bare); err != nil {
		t.Errorf("a cell-less notebook must be allowed: %v", err)
	}

	// Mixed: one runnable cell is enough. Refusing the whole file would be stricter than
	// the format, which decides per cell.
	mixed := filepath.Join(dir, "mixed.md")
	src := "---\nnotekit: 1\n---\n\n## a\n\n```sql\nSELECT 1;\n```\n\n## b\n\n```sh\n```\n"
	if err := os.WriteFile(mixed, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testTool().CheckEngine(mixed); err != nil {
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

// TestToolsListsThisTool keeps internal/siblings honest. It has to live here rather than
// beside the list, because a main package cannot be imported — and it is the list's only
// guard: a wrong Lang there would quietly send someone to a tool that refuses the file in
// turn, which is exactly the bug that prompted unifying it (sqlnote used to suggest clinote
// for `bash` cells, which clinote also refuses). Each tool checks its own entry; between them
// the whole list is verified.
func TestToolsListsThisTool(t *testing.T) {
	var found bool
	for _, peer := range siblings.All {
		if peer.Name != "clinote" {
			continue
		}
		found = true
		if peer.Lang != Lang {
			t.Errorf("siblings.All says clinote runs %q, the executor claims %q",
				peer.Lang, Lang)
		}
	}
	if !found {
		t.Errorf("siblings.All has no entry for clinote; the suggestion machinery " +
			"has no other source of truth")
	}
}

// testTool mirrors the [notetool.Tool] value main builds, so these tests exercise the same
// configuration the binary runs with.
func testTool() notetool.Tool {
	return notetool.Tool{Name: "clinote", Lang: Lang, Peers: siblings.All}
}
