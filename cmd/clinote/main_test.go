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

func TestFindNotebooks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.md", front+"## A\n\n```sh\necho x\n```\n")
	writeFile(t, dir, "two.md", "---\nnotekit: 1\n---\n\nprose\n")
	// Not candidates: no front matter, wrong version, not markdown.
	writeFile(t, dir, "plain.md", "# Just markdown\n")
	writeFile(t, dir, "future.md", "---\nnotekit: 2\n---\n")
	writeFile(t, dir, "notes.txt", front+"## A\n\n```sh\nx\n```\n")
	if err := os.Mkdir(filepath.Join(dir, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := findNotebooks(dir)
	if err != nil {
		t.Fatalf("findNotebooks: %v", err)
	}
	var names []string
	for _, p := range found {
		names = append(names, filepath.Base(p))
	}
	want := []string{"one.md", "two.md"}
	if len(names) != len(want) {
		t.Fatalf("found %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("found[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestFindNotebooksMissingDir(t *testing.T) {
	if _, err := findNotebooks(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("want error")
	}
}

// TestResolveNotebookPicker covers the no-argument path. The picker is deliberately not
// interactive: with one candidate the answer is obvious, and with several the useful
// thing is to name them rather than guess.
func TestResolveNotebookPicker(t *testing.T) {
	t.Run("exactly one is chosen", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "only.md", front+"## A\n\n```sh\nx\n```\n")
		inDir(t, dir, func() {
			got, err := resolveNotebook("")
			if err != nil {
				t.Fatalf("resolveNotebook: %v", err)
			}
			if filepath.Base(got) != "only.md" {
				t.Errorf("got %q, want only.md", got)
			}
		})
	})

	t.Run("several are listed rather than guessed", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.md", front+"## A\n\n```sh\nx\n```\n")
		writeFile(t, dir, "b.md", front+"## B\n\n```sh\nx\n```\n")
		inDir(t, dir, func() {
			_, err := resolveNotebook("")
			if err == nil {
				t.Fatal("want an error naming the candidates")
			}
			for _, want := range []string{"a.md", "b.md"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
		})
	})

	t.Run("none explains what to do", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "plain.md", "# not a notebook\n")
		inDir(t, dir, func() {
			_, err := resolveNotebook("")
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "notekit: 1") {
				t.Errorf("err = %v, want it to say how to make one", err)
			}
		})
	})
}

func TestResolveNotebookExplicitPath(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, dir, "good.md", front+"## A\n\n```sh\nx\n```\n")
	bad := writeFile(t, dir, "bad.md", "# not a notebook\n")

	if got, err := resolveNotebook(good); err != nil || got != good {
		t.Errorf("resolveNotebook(%q) = %q, %v", good, got, err)
	}
	// A named file that is not a notebook is refused with the reason, rather than
	// silently falling back to the picker.
	err := resolveNotebookErr(t, bad)
	if !strings.Contains(err.Error(), "not a notekit notebook") {
		t.Errorf("err = %v", err)
	}
	if _, err := resolveNotebook(filepath.Join(dir, "nope.md")); err == nil {
		t.Error("want error for a missing file")
	}
}

func resolveNotebookErr(t *testing.T, path string) error {
	t.Helper()
	_, err := resolveNotebook(path)
	if err == nil {
		t.Fatalf("resolveNotebook(%q) = nil error", path)
	}
	return err
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
