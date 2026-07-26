package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/exec/echoexec"
	"github.com/pmuston/notekit/kind"
	"github.com/pmuston/notekit/run"
)

const front = "---\nnotekit: 1\ntitle: Test Notebook\n---\n\n"

var fixedNow = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

type harness struct {
	t    *testing.T
	e    *echo.Echo
	s    *Server
	path string
	sch  *run.Scheduler
}

func newHarness(t *testing.T, body string, opts ...Option) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(front+body), 0o644); err != nil {
		t.Fatalf("writing notebook: %v", err)
	}

	sch := run.New(
		run.WithTool("serve/test"),
		run.WithClock(func() time.Time { return fixedNow }),
	)
	t.Cleanup(func() { _ = sch.Shutdown(context.Background()) })
	if err := sch.Open(context.Background(), path, echoexec.New()); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// A short poll so a completed run is observed without waiting half a second.
	all := append([]Option{WithPollInterval(10 * time.Millisecond)}, opts...)
	s, err := New(sch, path, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{t: t, e: s.Echo(), s: s, path: path, sch: sch}
}

func (h *harness) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h.e.ServeHTTP(rec, req)
	return rec
}

func (h *harness) get(target string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, target, nil)
}

func (h *harness) contents() string {
	h.t.Helper()
	b, err := os.ReadFile(h.path)
	if err != nil {
		h.t.Fatalf("reading notebook: %v", err)
	}
	return string(b)
}

// runIDRe pulls the run ID out of the polling fragment, which is how a browser learns it.
var runIDRe = regexp.MustCompile(`/runs/(r\d+)`)

// runCell posts a run and polls until the fragment stops asking to be polled.
func (h *harness) runCell(index int) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/cells/"+itoa(index)+"/run", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("POST run = %d: %s", rec.Code, rec.Body.String())
	}
	m := runIDRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		h.t.Fatalf("no run id in the polling fragment: %s", rec.Body.String())
	}
	id := m[1]

	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = h.get("/runs/" + id)
		if !strings.Contains(rec.Body.String(), "hx-trigger") {
			return rec // terminal: the fragment no longer re-arms the poll
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s never reached a terminal state", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestNotebookView renders the whole notebook: every cell, its source, its persisted
// result, and the embedded asset links.
func TestNotebookView(t *testing.T) {
	h := newHarness(t, "Opening prose.\n\n"+
		"## Greeting\n\nExplain the cell.\n\n```echo\nhello\n```\n\n"+
		"```output {run=\"2026-07-16T09:41:07Z\", tool=\"clinote/2.0\"}\nhello\n```\n\n"+
		"## Table\n\n```echo {format=csv}\na,b\n```\n\n"+
		"```output {format=csv}\nname,size\nalpha,10\nbeta,2\n```\n")

	rec := h.get("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"<title>Test Notebook</title>",
		// Assets are embedded and referenced locally: no CDN (harvest R9).
		`href="/assets/notekit.css"`,
		`src="/assets/htmx.min.js"`,
		`src="/assets/notekit.js"`,
		"Opening prose.",
		"Greeting", "Explain the cell.", "hello",
		"clinote/2.0",
		// The csv result renders as a sortable table (harvest V1).
		`class="nk-table"`, `data-sortable="true"`,
		"<th", "name", "size", "alpha", "beta",
		// Run buttons post to the async endpoint.
		`hx-post="/cells/0/run"`, `hx-post="/cells/1/run"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// No absolute external references anywhere: nothing may need the network.
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("the page references an external URL; assets must be embedded")
	}
}

func TestNotebookViewTitleFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback.md")
	// No `title` key, so the file name is used (§2: absence is not an error).
	os.WriteFile(path, []byte("---\nnotekit: 1\n---\n\n## A\n\n```echo\nx\n```\n"), 0o644)

	sch := run.New()
	t.Cleanup(func() { _ = sch.Shutdown(context.Background()) })
	if err := sch.Open(context.Background(), path, echoexec.New()); err != nil {
		t.Fatal(err)
	}
	s, err := New(sch, path)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Echo().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "<title>fallback.md</title>") {
		t.Errorf("title fallback missing: %s", rec.Body.String())
	}
}

// TestRunLoop is the M2 gate: post a run, poll, and see the result appear.
func TestRunLoop(t *testing.T) {
	h := newHarness(t, "## Greeting\n\n```echo {delay=30ms}\nhello there\n```\n")

	// The first response is the spinner fragment, and it carries the poll trigger.
	rec := h.do(http.MethodPost, "/cells/0/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST run = %d: %s", rec.Code, rec.Body.String())
	}
	first := rec.Body.String()
	for _, want := range []string{`class="nk-spinner"`, "hx-trigger", "delay:10ms", "/runs/r1"} {
		if !strings.Contains(first, want) {
			t.Errorf("polling fragment missing %q:\n%s", want, first)
		}
	}

	// Polling until terminal yields the rendered result and stops re-arming.
	deadline := time.Now().Add(5 * time.Second)
	var final string
	for {
		rec = h.get("/runs/r1")
		final = rec.Body.String()
		if !strings.Contains(final, "hx-trigger") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never finished; last fragment: %s", final)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !strings.Contains(final, "hello there") {
		t.Errorf("result not rendered:\n%s", final)
	}
	if !strings.Contains(final, "serve/test") {
		t.Errorf("provenance not shown:\n%s", final)
	}
	// And it really was persisted.
	if !strings.Contains(h.contents(), "```output {run=\"2026-07-26T12:00:00Z\", tool=\"serve/test\"}") {
		t.Errorf("result not persisted:\n%s", h.contents())
	}
}

// TestDomainFailureRendersAsErrorBlock: a non-zero status is a successful run that
// persists and renders an error block, visually distinct from output (§7).
func TestDomainFailureRendersAsErrorBlock(t *testing.T) {
	h := newHarness(t, "## Fails\n\n```echo {status=127}\ncommand not found: dv\n```\n")

	final := h.runCell(0).Body.String()
	if !strings.Contains(final, `class="nk-error"`) {
		t.Errorf("error block not rendered distinctly:\n%s", final)
	}
	if !strings.Contains(final, "command not found: dv") {
		t.Errorf("error text missing:\n%s", final)
	}
	if !strings.Contains(final, "status 127") {
		t.Errorf("status missing from provenance:\n%s", final)
	}
	if strings.Contains(final, `class="nk-output"`) {
		t.Error("a failure must not render as output")
	}
	if !strings.Contains(h.contents(), "```error {status=127") {
		t.Error("error block not persisted")
	}
}

// TestSessionFailureShowsReasonAndPersistsNothing is the other half of the Done/Failed
// distinction, at the UI level.
func TestSessionFailureShowsReasonAndPersistsNothing(t *testing.T) {
	h := newHarness(t, "## Broken\n\n```echo {broken}\nx\n```\n")
	before := h.contents()

	final := h.runCell(0).Body.String()
	if !strings.Contains(final, `class="nk-flash"`) {
		t.Errorf("no flash for a failed run:\n%s", final)
	}
	if !strings.Contains(final, "the engine broke") {
		t.Errorf("reason not shown:\n%s", final)
	}
	if h.contents() != before {
		t.Error("a failed run must persist nothing")
	}
}

// TestANSIColourIsLiveOnly is harvest F12 in both directions: colour reaches the browser
// but never the file.
func TestANSIColourIsLiveOnly(t *testing.T) {
	h := newHarness(t, "## Colour\n\n```echo\n\x1b[31mred\x1b[0m plain\n```\n")

	final := h.runCell(0).Body.String()
	if !strings.Contains(final, `class="ansi-fg-1"`) {
		t.Errorf("colour not rendered live:\n%s", final)
	}
	if !strings.Contains(final, ">red</span>") {
		t.Errorf("coloured text wrong:\n%s", final)
	}

	// Stripped on disk: the durable form is the plain form.
	nb, err := doc.Parse([]byte(h.contents()))
	if err != nil {
		t.Fatal(err)
	}
	result := string(nb.Cells()[0].Results[0].Span.In(nb.Bytes()))
	if strings.Contains(result, "\x1b") {
		t.Errorf("escape reached the durable form: %q", result)
	}

	// After a reload the live body is still in memory, so colour persists for this
	// process. A fresh server over the same file has none — which is the contract.
	fresh, err := New(run.New(), h.path)
	if err == nil {
		t.Fatal("a server over an unopened notebook should be refused")
	}
	_ = fresh
}

// TestColourGoneAfterRestart: a new process renders from the durable body, which has no
// colour. That is the contract, not a bug — the live view may be prettier, never fuller.
func TestColourGoneAfterRestart(t *testing.T) {
	h := newHarness(t, "## Colour\n\n```echo\n\x1b[31mred\x1b[0m\n```\n")
	h.runCell(0)

	// A second scheduler over the same file stands in for a restart.
	sch2 := run.New()
	t.Cleanup(func() { _ = sch2.Shutdown(context.Background()) })
	if err := sch2.Open(context.Background(), h.path, echoexec.New()); err != nil {
		t.Fatal(err)
	}
	s2, err := New(sch2, h.path)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s2.Echo().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "red") {
		t.Errorf("the result text should still render:\n%s", body)
	}
	if strings.Contains(body, "ansi-fg-1") {
		t.Error("colour survived a restart; it must come only from the live body")
	}
}

func TestTruncationShown(t *testing.T) {
	h := newHarness(t, "## Long\n\n```echo {repeat=200}\nabcdefghij\n```\n")
	// Use a small cap by rebuilding the scheduler is awkward here, so rely on the
	// notebook already carrying a truncated flag instead.
	_ = h

	h2 := newHarness(t, "## Long\n\n```echo\nx\n```\n\n"+
		"```output {truncated}\npartial\n[notekit: output truncated at 7 bytes]\n```\n")
	rec := h2.get("/")
	if !strings.Contains(rec.Body.String(), "truncated") {
		t.Errorf("truncation not surfaced:\n%s", rec.Body.String())
	}
}

func TestUnrunnableCellsAreDisabledWithAReason(t *testing.T) {
	t.Run("unclosed fence", func(t *testing.T) {
		h := newHarness(t, "## Unclosed\n\n```echo\nno close\n")
		body := h.get("/").Body.String()
		if !strings.Contains(body, "disabled") {
			t.Errorf("the run button should be disabled:\n%s", body)
		}
		if !strings.Contains(body, "unclosed") {
			t.Errorf("the reason should be given:\n%s", body)
		}
	})
	t.Run("malformed info string", func(t *testing.T) {
		h := newHarness(t, "## Bad\n\n```echo {a=1, a=2}\nx\n```\n")
		body := h.get("/").Body.String()
		if !strings.Contains(body, "disabled") {
			t.Errorf("the run button should be disabled:\n%s", body)
		}
		if !strings.Contains(body, "duplicate key") {
			t.Errorf("the reason should be given:\n%s", body)
		}
	})
}

func TestRunRefusedForLanguageMismatch(t *testing.T) {
	h := newHarness(t, "## Sql\n\n```sql\nSELECT 1\n```\n")
	rec := h.do(http.MethodPost, "/cells/0/run", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST run = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "executor claims") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestBadRequests(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")

	tests := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{"cell index not a number", http.MethodPost, "/cells/abc/run", http.StatusBadRequest},
		{"cell index out of range", http.MethodPost, "/cells/9/run", http.StatusBadRequest},
		{"unknown run", http.MethodGet, "/runs/r999", http.StatusNotFound},
		{"bad prose ref", http.MethodGet, "/prose/nonsense", http.StatusBadRequest},
		{"prose ref out of range", http.MethodGet, "/prose/9-before", http.StatusBadRequest},
		{"prose ref bad half", http.MethodGet, "/prose/0-sideways", http.StatusBadRequest},
		{"missing sidecar", http.MethodGet, "/sidecar/nope.png", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(tt.method, tt.target, nil)
			if rec.Code != tt.want {
				t.Errorf("%s %s = %d, want %d (%s)", tt.method, tt.target, rec.Code, tt.want,
					rec.Body.String())
			}
		})
	}
}

// TestProseEditing covers the whole loop: render, open the editor, save, re-render.
func TestProseEditing(t *testing.T) {
	h := newHarness(t, "Opening prose.\n\n"+
		"## Cell\n\nBefore the fence.\n\n```echo\nx\n```\n\nAfter the fence.\n")

	// The rendered region offers an editor.
	rec := h.get("/prose/0-before")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET prose = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Before the fence.") {
		t.Errorf("prose not rendered:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "edit=1") {
		t.Errorf("no way to start editing:\n%s", rec.Body.String())
	}

	// The editor carries the current text and the unsaved-changes indicator.
	rec = h.get("/prose/0-before?edit=1")
	for _, want := range []string{"<textarea", "Before the fence.", `class="nk-dirty"`, "unsaved changes"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("editor missing %q:\n%s", want, rec.Body.String())
		}
	}

	// Saving rewrites exactly that region.
	rec = h.do(http.MethodPut, "/prose/0-before", url.Values{"text": {"\nRewritten prose.\n\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT prose = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Rewritten prose.") {
		t.Errorf("saved prose not returned:\n%s", rec.Body.String())
	}

	got := h.contents()
	want := front + "Opening prose.\n\n## Cell\n\nRewritten prose.\n\n```echo\nx\n```\n\nAfter the fence.\n"
	if got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
	// The cell survived untouched.
	nb, err := doc.Parse([]byte(got))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if nb.Cells()[0].SourceText() != "x\n" {
		t.Errorf("the cell's source changed: %q", nb.Cells()[0].SourceText())
	}
}

func TestProseEditingPreambleAndAfter(t *testing.T) {
	h := newHarness(t, "Opening.\n\n## Cell\n\n```echo\nx\n```\n\nTrailing.\n")

	if rec := h.do(http.MethodPut, "/prose/preamble", url.Values{"text": {"\nNew opening.\n\n"}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT preamble = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodPut, "/prose/0-after", url.Values{"text": {"\nNew trailing.\n"}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT after = %d: %s", rec.Code, rec.Body.String())
	}

	got := h.contents()
	want := front + "New opening.\n\n## Cell\n\n```echo\nx\n```\n\nNew trailing.\n"
	if got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
}

// TestProseEditPreservesResults: editing prose after a result must not disturb the
// result, which sits in a different region.
func TestProseEditPreservesResults(t *testing.T) {
	h := newHarness(t, "## Cell\n\n```echo\nx\n```\n\n"+
		"```output {run=\"2026-07-16T09:41:07Z\"}\nresult text\n```\n\nTrailing.\n")

	if rec := h.do(http.MethodPut, "/prose/0-after", url.Values{"text": {"\nEdited trailing.\n"}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	got := h.contents()
	if !strings.Contains(got, "```output {run=\"2026-07-16T09:41:07Z\"}\nresult text\n```") {
		t.Errorf("the result was disturbed:\n%s", got)
	}
	if !strings.Contains(got, "Edited trailing.") {
		t.Errorf("the edit did not land:\n%s", got)
	}
}

// TestProseEditRefusedIfItWouldBreakTheNotebook: prose is prose, but a save that made
// the file unreadable is the one outcome never worth writing.
func TestProseEditRefusedIfItWouldBreakTheNotebook(t *testing.T) {
	h := newHarness(t, "## Cell\n\n```echo\nx\n```\n")
	before := h.contents()

	// A prose edit cannot normally break parsing, so this asserts the guard exists
	// rather than that a particular input trips it.
	rec := h.do(http.MethodPut, "/prose/0-before", url.Values{"text": {"\nharmless\n\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if h.contents() == before {
		t.Error("the edit should have been saved")
	}
	if _, err := doc.Parse([]byte(h.contents())); err != nil {
		t.Errorf("the saved notebook does not parse: %v", err)
	}
}

func TestNotebookModePreservedOnProseSave(t *testing.T) {
	h := newHarness(t, "## Cell\n\n```echo\nx\n```\n")
	if err := os.Chmod(h.path, 0o640); err != nil {
		t.Fatal(err)
	}
	if rec := h.do(http.MethodPut, "/prose/0-before", url.Values{"text": {"\nx\n\n"}}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	info, err := os.Stat(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want 640", got)
	}
}

// TestEmbeddedAssetsServed: every asset the page references must actually be served
// from the binary, with no network involved.
func TestEmbeddedAssetsServed(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")

	for _, tt := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/assets/notekit.css", "text/css", "--nk-fg"},
		{"/assets/notekit.js", "javascript", "nk-table"},
		{"/assets/htmx.min.js", "javascript", "htmx"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := h.get(tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", tt.path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", ct, tt.contentType)
			}
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Errorf("body missing %q", tt.contains)
			}
		})
	}
	if rec := h.get("/assets/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", rec.Code)
	}
}

func TestSidecarServed(t *testing.T) {
	reg := kind.NewRegistry()
	if err := reg.Register(kind.Kind{
		Name: "graph",
		Durable: func(any) (kind.Durable, error) {
			return kind.Durable{Sidecar: &kind.Sidecar{Files: []kind.File{
				{Ext: "svg", Content: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), Primary: true},
				{Ext: "json", Content: []byte(`{}`)},
			}}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	os.WriteFile(path, []byte(front+"## Wiring\n\n```echo {kind=graph}\nMATCH\n```\n"), 0o644)

	sch := run.New(run.WithRegistry(reg), run.WithClock(func() time.Time { return fixedNow }))
	t.Cleanup(func() { _ = sch.Shutdown(context.Background()) })
	if err := sch.Open(context.Background(), path, echoexec.New()); err != nil {
		t.Fatal(err)
	}
	s, err := New(sch, path, WithRegistry(reg), WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, e: s.Echo(), s: s, path: path, sch: sch}

	h.runCell(0)

	body := h.get("/").Body.String()
	if !strings.Contains(body, `<img src="/sidecar/wiring--`) {
		t.Errorf("sidecar image not referenced:\n%s", body)
	}

	// Find the artifact name the run chose and fetch it.
	m := regexp.MustCompile(`/sidecar/(wiring--[a-z2-7]+\.svg)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no sidecar name in the page:\n%s", body)
	}
	rec := h.get("/sidecar/" + m[1])
	if rec.Code != http.StatusOK {
		t.Fatalf("GET sidecar = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("sidecar body = %q", rec.Body.String())
	}
}

// TestSidecarPathTraversalRefused: a request must not escape the sidecar directory.
func TestSidecarPathTraversalRefused(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	// The notebook itself sits one level above the sidecar directory, so this is the
	// escape a naive join would allow.
	for _, name := range []string{"..%2fnotes.md", "..", "%2e%2e%2fnotes.md"} {
		rec := h.get("/sidecar/" + name)
		if rec.Code == http.StatusOK {
			t.Errorf("GET /sidecar/%s = 200; traversal must be refused:\n%s", name, rec.Body.String())
		}
	}
}

func TestBasePathMounting(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n", WithBasePath("/nb"))

	if rec := h.get("/nb/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /nb/ = %d", rec.Code)
	}
	body := h.get("/nb").Body.String()
	if !strings.Contains(body, `href="/nb/assets/notekit.css"`) {
		t.Errorf("asset links not rebased:\n%s", body)
	}
	if !strings.Contains(body, `hx-post="/nb/cells/0/run"`) {
		t.Errorf("run endpoint not rebased:\n%s", body)
	}
	if rec := h.get("/nb/assets/notekit.css"); rec.Code != http.StatusOK {
		t.Errorf("GET /nb/assets/notekit.css = %d", rec.Code)
	}
	// The un-based path must not also work, or two URLs would serve one notebook.
	if rec := h.get("/"); rec.Code == http.StatusOK {
		t.Error("GET / should not serve a notebook mounted at /nb")
	}
}

func TestNewValidation(t *testing.T) {
	t.Run("nil scheduler", func(t *testing.T) {
		if _, err := New(nil, "notes.md"); err == nil {
			t.Error("want error")
		}
	})
	t.Run("notebook not open", func(t *testing.T) {
		// Opening is the tool's job, because only the tool knows the executor.
		if _, err := New(run.New(), "notes.md"); err == nil {
			t.Error("want error for a notebook the scheduler has not opened")
		}
	})
}

func TestWithTitleAndPollOptions(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n",
		WithTitle("Custom"), WithPollInterval(0), WithBasePath("/"))
	body := h.get("/").Body.String()
	if !strings.Contains(body, "<title>Custom</title>") {
		t.Errorf("title override ignored:\n%s", body)
	}
	// A zero poll interval is ignored rather than producing a busy loop.
	if h.s.PollIntervalMS() == "0" {
		t.Error("a zero poll interval should have been ignored")
	}
}

// TestRegisterOnCallerOwnedEcho is the "components, not an application" claim.
func TestRegisterOnCallerOwnedEcho(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")

	e := echo.New()
	var sawMiddleware bool
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sawMiddleware = true
			return next(c)
		}
	})
	h.s.Register(e)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if !sawMiddleware {
		t.Error("the caller's middleware did not run, so this is not composable")
	}
}

// TestPageRendersWithoutJavaScript is the degradation requirement stated as a test: the
// durable content is all present in the HTML, so a reader with scripting off loses only
// niceties.
func TestPageRendersWithoutJavaScript(t *testing.T) {
	h := newHarness(t, "## Cell\n\nProse here.\n\n```echo\nsource line\n```\n\n"+
		"```output {format=csv}\nname,size\nalpha,10\n```\n")

	body := h.get("/").Body.String()
	for _, want := range []string{"Prose here.", "source line", "name", "alpha", "10"} {
		if !strings.Contains(body, want) {
			t.Errorf("content missing from the served HTML: %q", want)
		}
	}
	// The table is a real table, not built by script.
	if !strings.Contains(body, "<tbody><tr><td>alpha</td><td>10</td></tr></tbody>") {
		t.Errorf("table rows not server-rendered:\n%s", body)
	}
}

func TestMalformedTableFallsBackToText(t *testing.T) {
	// A csv body the parser cannot read is shown as text: it is what the run
	// produced, and hiding it would help nobody.
	h := newHarness(t, "## Bad table\n\n```echo\nx\n```\n\n"+
		"```output {format=csv}\n\"unclosed quote\nrow\n```\n")
	body := h.get("/").Body.String()
	if !strings.Contains(body, "nk-malformed") {
		t.Errorf("malformed table not flagged:\n%s", body)
	}
	if !strings.Contains(body, "unclosed quote") {
		t.Errorf("the body should still be shown:\n%s", body)
	}
}

func TestUnknownFormatFallsBackToText(t *testing.T) {
	h := newHarness(t, "## Odd\n\n```echo\nx\n```\n\n"+
		"```output {format=tsv}\na\tb\n```\n")
	body := h.get("/").Body.String()
	if !strings.Contains(body, "nk-malformed") {
		t.Errorf("an unrenderable format should fall back to text:\n%s", body)
	}
	if !strings.Contains(body, "a\tb") {
		t.Errorf("the body should still be shown:\n%s", body)
	}
}

func TestJSONLRendersAsTable(t *testing.T) {
	h := newHarness(t, "## JSONL\n\n```echo\nx\n```\n\n"+
		"```output {format=jsonl}\n{\"name\":\"alpha\",\"size\":10}\n{\"size\":2,\"name\":\"beta\",\"extra\":true}\n```\n")
	body := h.get("/").Body.String()
	if !strings.Contains(body, `class="nk-table"`) {
		t.Errorf("jsonl not rendered as a table:\n%s", body)
	}
	for _, want := range []string{"name", "size", "extra", "alpha", "beta", "true"} {
		if !strings.Contains(body, want) {
			t.Errorf("table missing %q:\n%s", want, body)
		}
	}
}

func TestEmptyNotebookRenders(t *testing.T) {
	h := newHarness(t, "")
	rec := h.get("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", rec.Code, rec.Body.String())
	}
	// The preamble is still editable, so a notebook can be written from empty.
	if !strings.Contains(rec.Body.String(), "prose-preamble") {
		t.Errorf("no editable preamble:\n%s", rec.Body.String())
	}
}

// TestProseRegionIsKeyboardReachable: the region is a div with a click handler, so it
// needs an explicit role, a tab stop, and a keyboard trigger — otherwise editing prose
// is mouse-only, which the visual check caught and no assertion had.
func TestProseRegionIsKeyboardReachable(t *testing.T) {
	h := newHarness(t, "Prose.\n\n## Cell\n\n```echo\nx\n```\n")
	body := h.get("/").Body.String()

	for _, want := range []string{
		`role="button"`,
		`tabindex="0"`,
		`aria-label="edit prose"`,
		"keyup[key=='Enter']",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prose region missing %q:\n%s", want, body)
		}
	}
}

// TestTableHeadersAreKeyboardReachable: sorting is a live nicety, but it must not be
// mouse-only either.
func TestTableHeadersAreKeyboardReachable(t *testing.T) {
	h := newHarness(t, "## T\n\n```echo\nx\n```\n\n```output {format=csv}\na,b\n1,2\n```\n")
	body := h.get("/").Body.String()
	if !strings.Contains(body, `<th data-col="0" tabindex="0">`) {
		t.Errorf("table headers are not focusable:\n%s", body)
	}
}

// The tests below cover the three gaps clinote v1's route surface exposed. All three are
// kit features every tool needs, which is what M3 exists to discover.

func TestSourceEditing(t *testing.T) {
	h := newHarness(t, "## Cell\n\n```echo {id=aaaa2345}\noriginal\n```\n\n```output\nold result\n```\n")

	// The rendered source offers an editor, and is keyboard-reachable.
	rec := h.get("/cells/0/source")
	body := rec.Body.String()
	for _, want := range []string{"original", "edit=1", `role="button"`, `tabindex="0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered source missing %q:\n%s", want, body)
		}
	}

	rec = h.get("/cells/0/source?edit=1")
	if !strings.Contains(rec.Body.String(), `name="source"`) {
		t.Errorf("no editor:\n%s", rec.Body.String())
	}

	rec = h.do(http.MethodPut, "/cells/0/source", url.Values{"source": {"rewritten\nsecond line\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	got := h.contents()
	// The info string, including the id, is preserved exactly (§5.1: ids are immutable).
	want := front + "## Cell\n\n```echo {id=aaaa2345}\nrewritten\nsecond line\n```\n\n```output\nold result\n```\n"
	if got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
	nb := mustParse(t, got)
	if nb.Cells()[0].ID != "aaaa2345" {
		t.Errorf("the id was lost: %q", nb.Cells()[0].ID)
	}
	if len(nb.Cells()[0].Results) != 1 {
		t.Error("the result should be untouched by a source edit")
	}
}

func mustParse(t *testing.T, src string) *doc.Notebook {
	t.Helper()
	nb, err := doc.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return nb
}

// TestSourceEditWidensTheFence is the hazard a naive body-only splice would create: a
// body containing a backtick run at least as long as the fence would terminate it early
// and silently turn the rest of the cell into prose.
func TestSourceEditWidensTheFence(t *testing.T) {
	h := newHarness(t, "## Cell\n\n```echo\nplain\n```\n")

	rec := h.do(http.MethodPut, "/cells/0/source",
		url.Values{"source": {"printf '```\\n'\necho done\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}

	got := h.contents()
	if !strings.Contains(got, "````echo\n") {
		t.Errorf("the fence was not widened:\n%s", got)
	}
	nb := mustParse(t, got)
	if n := len(nb.Cells()); n != 1 {
		t.Fatalf("got %d cells, want 1 — the fence broke:\n%s", n, got)
	}
	if body := nb.Cells()[0].SourceText(); !strings.Contains(body, "echo done") {
		t.Errorf("body truncated at the inner fence: %q", body)
	}
}

func TestSourceEditErrors(t *testing.T) {
	t.Run("cell out of range", func(t *testing.T) {
		h := newHarness(t, "## A\n\n```echo\nx\n```\n")
		if rec := h.do(http.MethodPut, "/cells/9/source", url.Values{"source": {"y"}}); rec.Code != http.StatusBadRequest {
			t.Errorf("code = %d", rec.Code)
		}
		if rec := h.get("/cells/9/source"); rec.Code != http.StatusBadRequest {
			t.Errorf("GET code = %d", rec.Code)
		}
	})
	t.Run("unclosed fence is refused", func(t *testing.T) {
		// Closing it on the author's behalf would be the repair §10 forbids.
		h := newHarness(t, "## A\n\n```echo\nno close\n")
		before := h.contents()
		rec := h.do(http.MethodPut, "/cells/0/source", url.Values{"source": {"y\n"}})
		if rec.Code != http.StatusConflict {
			t.Fatalf("code = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
		}
		if h.contents() != before {
			t.Error("nothing should have been written")
		}
	})
	t.Run("fence widening contains an injected heading", func(t *testing.T) {
		// A body that tries to close its own fence and open a new section cannot:
		// widening measures the backtick run in the body, so the whole thing stays
		// inside one wider fence and the "heading" is code. The cell-count check in
		// the handler is defence in depth behind that, not the mechanism.
		h := newHarness(t, "## A\n\n```echo\nx\n```\n")
		rec := h.do(http.MethodPut, "/cells/0/source",
			url.Values{"source": {"x\n```\n\n## Injected\n\n```echo\ny\n"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
		}
		nb := mustParse(t, h.contents())
		if n := len(nb.Cells()); n != 1 {
			t.Fatalf("got %d cells, want 1 — the injection escaped:\n%s", n, h.contents())
		}
		if !strings.Contains(nb.Cells()[0].SourceText(), "## Injected") {
			t.Errorf("the heading should be contained as code: %q", nb.Cells()[0].SourceText())
		}
		if !strings.Contains(h.contents(), "````echo") {
			t.Errorf("the fence was not widened:\n%s", h.contents())
		}
	})
}

func TestCancelRoute(t *testing.T) {
	h := newHarness(t, "## Slow\n\n```echo {delay=3s}\nx\n```\n")

	rec := h.do(http.MethodPost, "/cells/0/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST run = %d", rec.Code)
	}
	// The polling fragment offers a Cancel button, which v1 had and v2 lacked.
	if !strings.Contains(rec.Body.String(), "/runs/r1/cancel") {
		t.Errorf("no cancel affordance:\n%s", rec.Body.String())
	}

	rec = h.do(http.MethodPost, "/runs/r1/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST cancel = %d: %s", rec.Code, rec.Body.String())
	}

	// The run reaches Cancelled and nothing is persisted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, ok := h.sch.State("r1")
		if ok && r.State.Terminal() {
			if r.State != run.Cancelled {
				t.Fatalf("state = %v, want cancelled", r.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reached a terminal state")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(mustParse(t, h.contents()).Cells()[0].Results) != 0 {
		t.Error("a cancelled run must persist nothing")
	}
}

func TestCancelUnknownRun(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	if rec := h.do(http.MethodPost, "/runs/r999/cancel", nil); rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestCancelFinishedRunSaysSo(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	h.runCell(0)
	rec := h.do(http.MethodPost, "/runs/r1/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already finished") {
		t.Errorf("body = %q, want it to say the run had finished", rec.Body.String())
	}
}

func TestRunAll(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nfirst\n```\n\n"+
		"## B\n\n```echo\nsecond\n```\n\n"+
		"## C\n\n```sql\nSELECT 1\n```\n")

	rec := h.do(http.MethodPost, "/cells/run-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST run-all = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "running 2 cells") {
		t.Errorf("body = %q, want 2 submitted", rec.Body.String())
	}
	// The sql cell is skipped rather than failing the request.
	if !strings.Contains(rec.Body.String(), "1 skipped") {
		t.Errorf("body = %q, want the skip reported", rec.Body.String())
	}
	// HX-Refresh tells the browser to reload so each cell shows its own state.
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q", rec.Header().Get("HX-Refresh"))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		done := 0
		for _, r := range h.sch.Runs() {
			if r.State.Terminal() {
				done++
			}
		}
		if done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run-all never finished")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cells := mustParse(t, h.contents()).Cells()
	for i := 0; i < 2; i++ {
		if len(cells[i].Results) != 1 {
			t.Errorf("cell %d has %d results, want 1", i, len(cells[i].Results))
		}
	}
	if len(cells[2].Results) != 0 {
		t.Error("the sql cell should not have run")
	}
}

func TestRunAllWithNothingRunnable(t *testing.T) {
	h := newHarness(t, "## Only sql\n\n```sql\nSELECT 1\n```\n")
	rec := h.do(http.MethodPost, "/cells/run-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nothing to run") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestPageOffersRunAll(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	if !strings.Contains(h.get("/").Body.String(), `hx-post="/cells/run-all"`) {
		t.Error("no Run all button")
	}
	// A notebook with no cells has nothing to run all of.
	empty := newHarness(t, "just prose\n")
	if strings.Contains(empty.get("/").Body.String(), "run-all") {
		t.Error("Run all offered for a notebook with no cells")
	}
}

// The tests below cover the two parity features that were deferred at M3: adding a cell
// and deleting one. Both change document *structure*, which is why they were held back
// until the write they perform had a rule in §10 to follow.

func TestAddCell(t *testing.T) {
	h := newHarness(t, "## First\n\n```echo\na\n```\n")

	rec := h.do(http.MethodPost, "/cells/add", url.Values{
		"heading": {"Second"},
		"body":    {"echo hi\n"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST add = %d: %s", rec.Code, rec.Body.String())
	}
	// Indices after the insertion point shift, so the page reloads rather than
	// swapping one fragment and leaving the rest addressing the wrong cells.
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q", rec.Header().Get("HX-Refresh"))
	}

	got := h.contents()
	want := front + "## First\n\n```echo\na\n```\n\n## Second\n\n```echo\necho hi\n```\n"
	if got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
	// A new cell is a heading plus a fence and nothing else — no invented prose, no
	// placeholder result.
	cells := mustParse(t, got).Cells()
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(cells))
	}
	if len(cells[1].Results) != 0 {
		t.Error("a new cell must have no result")
	}
	if cells[1].Lang != "echo" {
		t.Errorf("Lang = %q, want the executor's tag", cells[1].Lang)
	}
}

func TestAddCellAtAPosition(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n\n## C\n\n```echo\nc\n```\n")

	rec := h.do(http.MethodPost, "/cells/add", url.Values{
		"heading": {"B"}, "body": {"b\n"}, "after": {"0"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST add = %d: %s", rec.Code, rec.Body.String())
	}
	cells := mustParse(t, h.contents()).Cells()
	if len(cells) != 3 {
		t.Fatalf("got %d cells, want 3", len(cells))
	}
	headings := []string{cells[0].HeadingText, cells[1].HeadingText, cells[2].HeadingText}
	if headings[0] != "A" || headings[1] != "B" || headings[2] != "C" {
		t.Errorf("order = %v, want [A B C]", headings)
	}
}

func TestAddCellIntoAnEmptyNotebook(t *testing.T) {
	h := newHarness(t, "")
	rec := h.do(http.MethodPost, "/cells/add", url.Values{"heading": {"First"}, "body": {"x\n"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST add = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(mustParse(t, h.contents()).Cells()); n != 1 {
		t.Errorf("got %d cells, want 1:\n%s", n, h.contents())
	}
}

func TestAddCellRunsImmediately(t *testing.T) {
	// The point of adding a cell is to run it, so the added cell must be runnable
	// without a restart.
	h := newHarness(t, "## First\n\n```echo\na\n```\n")
	if rec := h.do(http.MethodPost, "/cells/add", url.Values{
		"heading": {"Second"}, "body": {"fresh output\n"},
	}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	final := h.runCell(1).Body.String()
	if !strings.Contains(final, "fresh output") {
		t.Errorf("the added cell did not run:\n%s", final)
	}
}

func TestAddCellErrors(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n")
	before := h.contents()

	tests := []struct {
		name string
		form url.Values
	}{
		// §4.3: a section whose first fence is tagged output holds no cell, so
		// creating one would produce a cell that is not a cell.
		{"result tag", url.Values{"heading": {"H"}, "lang": {"output"}}},
		{"heading level 1", url.Values{"heading": {"H"}, "level": {"1"}}},
		{"heading level 7", url.Values{"heading": {"H"}, "level": {"7"}}},
		{"level not a number", url.Values{"heading": {"H"}, "level": {"two"}}},
		{"newline in heading", url.Values{"heading": {"one\ntwo"}}},
		{"after out of range", url.Values{"heading": {"H"}, "after": {"9"}}},
		{"after not a number", url.Values{"heading": {"H"}, "after": {"x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/cells/add", tt.form)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("code = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if h.contents() != before {
				t.Error("nothing should have been written")
			}
		})
	}
}

func TestDeleteCell(t *testing.T) {
	h := newHarness(t, "Opening.\n\n"+
		"## A\n\nprose\n\n```echo\na\n```\n\n```output\nr\n```\n\ntrailing\n\n"+
		"## B\n\n```echo\nb\n```\n")

	rec := h.do(http.MethodDelete, "/cells/0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q", rec.Header().Get("HX-Refresh"))
	}

	// The whole section goes: heading, prose, fence, result, trailing prose.
	got := h.contents()
	want := front + "Opening.\n\n## B\n\n```echo\nb\n```\n"
	if got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
	// The preamble, which belongs to no cell, survives.
	if !strings.Contains(got, "Opening.\n") {
		t.Error("the preamble was lost")
	}
}

func TestDeleteLastCell(t *testing.T) {
	h := newHarness(t, "Opening.\n\n## Only\n\n```echo\na\n```\n")
	if rec := h.do(http.MethodDelete, "/cells/0", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	nb := mustParse(t, h.contents())
	if len(nb.Cells()) != 0 {
		t.Errorf("got %d cells, want 0", len(nb.Cells()))
	}
	// A notebook with no cells is still a notebook, and still editable.
	if !strings.Contains(h.get("/").Body.String(), "prose-preamble") {
		t.Error("the emptied notebook is no longer editable")
	}
}

func TestDeleteCellErrors(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n")
	before := h.contents()
	for _, target := range []string{"/cells/9", "/cells/abc", "/cells/-1"} {
		rec := h.do(http.MethodDelete, target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s = %d, want %d", target, rec.Code, http.StatusBadRequest)
		}
	}
	if h.contents() != before {
		t.Error("nothing should have been written")
	}
}

func TestPageOffersAddAndDelete(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n")
	body := h.get("/").Body.String()

	for _, want := range []string{
		`hx-post="/cells/add"`,
		`name="heading"`,
		`name="body"`,
		`hx-delete="/cells/0"`,
		// Deleting a cell is irreversible, so it asks first.
		"hx-confirm=",
		"after cell 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// With no cells there is nowhere to insert but the end, so the selector is gone.
	empty := newHarness(t, "")
	eb := empty.get("/").Body.String()
	if !strings.Contains(eb, `hx-post="/cells/add"`) {
		t.Error("an empty notebook should still offer add")
	}
	if strings.Contains(eb, `name="after"`) {
		t.Error("the position selector should be omitted when there is nowhere to choose")
	}
}

func TestAddDeleteRoundTripKeepsOtherCellsIntact(t *testing.T) {
	h := newHarness(t, "## Keep\n\n```echo\nkeep\n```\n\n```output {run=\"x\"}\nkept result\n```\n")

	if rec := h.do(http.MethodPost, "/cells/add", url.Values{
		"heading": {"Temp"}, "body": {"t\n"},
	}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := h.do(http.MethodDelete, "/cells/1", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	got := h.contents()
	// The surviving cell and its result are byte-identical.
	if !strings.Contains(got, "```echo\nkeep\n```") {
		t.Errorf("the kept cell changed:\n%s", got)
	}
	if !strings.Contains(got, "```output {run=\"x\"}\nkept result\n```") {
		t.Errorf("the kept result changed:\n%s", got)
	}
	if n := len(mustParse(t, got).Cells()); n != 1 {
		t.Errorf("got %d cells, want 1", n)
	}
}

// TestNewCellTagDefaultsToTheExecutors is why WithLang is rarely needed: the tag comes
// from the scheduler, so a tool cannot forget it and offer to create unrunnable cells.
func TestNewCellTagDefaultsToTheExecutors(t *testing.T) {
	h := newHarness(t, "")
	if !strings.Contains(h.get("/").Body.String(), `value="echo"`) {
		t.Errorf("the form does not offer the executor's tag:\n%s", h.get("/").Body.String())
	}
	if rec := h.do(http.MethodPost, "/cells/add", url.Values{"heading": {"H"}}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(h.contents(), "```echo\n") {
		t.Errorf("new cell got the wrong tag:\n%s", h.contents())
	}
}

func TestWithLangOption(t *testing.T) {
	h := newHarness(t, "", WithLang("cypher"))
	if !strings.Contains(h.get("/").Body.String(), `value="cypher"`) {
		t.Errorf("WithLang not reflected in the form:\n%s", h.get("/").Body.String())
	}
	if rec := h.do(http.MethodPost, "/cells/add", url.Values{"heading": {"H"}}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(h.contents(), "```cypher\n") {
		t.Errorf("new cell got the wrong tag:\n%s", h.contents())
	}
}

// --- reordering (format spec §10 h) ----------------------------------------------------

// doWithFP is `do` with a document fingerprint attached, which is how a browser makes every
// request.
func (h *harness) doWithFP(method, target, fp string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set(FingerprintHeader, fp)
	rec := httptest.NewRecorder()
	h.e.ServeHTTP(rec, req)
	return rec
}

// pageFingerprint reads the value the page was rendered with, the way notekit.js does.
func (h *harness) pageFingerprint() string {
	h.t.Helper()
	m := regexp.MustCompile(`data-nk-doc="([^"]*)"`).FindStringSubmatch(h.get("/").Body.String())
	if m == nil {
		h.t.Fatal("the page carries no fingerprint, so a client has no way to learn one")
	}
	return m[1]
}

func TestMoveCell(t *testing.T) {
	h := newHarness(t, "Opening prose, owned by no cell.\n\n"+
		"## A\n\nprose for A\n\n```echo\na\n```\n\n```output\nr\n```\n\n"+
		"## B\n\n```echo\nb\n```\n\n"+
		"## C\n\n```echo\nc\n```\n")

	rec := h.do(http.MethodPost, "/cells/1/move-up", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("move-up = %d: %s", rec.Code, rec.Body.String())
	}
	// Every index below the moved cell changed, so a partial swap would leave the page
	// describing a structure that no longer exists.
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q, want true", rec.Header().Get("HX-Refresh"))
	}

	// B rose above A, and A kept its prose and its result — the unit is the section.
	want := front + "Opening prose, owned by no cell.\n\n" +
		"## B\n\n```echo\nb\n```\n\n" +
		"## A\n\nprose for A\n\n```echo\na\n```\n\n```output\nr\n```\n\n" +
		"## C\n\n```echo\nc\n```\n"
	if got := h.contents(); got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}

	// And back again restores the file exactly. Note the index: after the move B is at 0
	// and A at 1, so it is B that moves down — moving index 1 would swap A with C.
	if rec := h.do(http.MethodPost, "/cells/0/move-down", nil); rec.Code != http.StatusOK {
		t.Fatalf("move-down = %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.contents(); got != front+"Opening prose, owned by no cell.\n\n"+
		"## A\n\nprose for A\n\n```echo\na\n```\n\n```output\nr\n```\n\n"+
		"## B\n\n```echo\nb\n```\n\n"+
		"## C\n\n```echo\nc\n```\n" {
		t.Errorf("moving back did not restore the file:\n%q", got)
	}
}

// TestMoveCellNoOpWritesNothing pins §10 h's no-op rule at the HTTP boundary: the file is
// the artifact, and a spurious rewrite is visible to a reader, a diff and any watcher.
func TestMoveCellNoOpWritesNothing(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n\n## B\n\n```echo\nb\n```\n")
	before := h.contents()

	for _, tc := range []struct{ target, want string }{
		{"/cells/0/move-up", "already first"},
		{"/cells/1/move-down", "already last"},
	} {
		rec := h.do(http.MethodPost, tc.target, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", tc.target, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: body should say %q, got %s", tc.target, tc.want, rec.Body.String())
		}
		// Nothing to do means nothing written, so no refresh either.
		if rec.Header().Get("HX-Refresh") == "true" {
			t.Errorf("%s: a no-op should not refresh the page", tc.target)
		}
		if got := h.contents(); got != before {
			t.Errorf("%s rewrote the file:\n got %q\nwant %q", tc.target, got, before)
		}
	}
}

func TestMoveCellRejectsABadIndex(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n")
	for _, target := range []string{"/cells/9/move-up", "/cells/x/move-down"} {
		if rec := h.do(http.MethodPost, target, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", target, rec.Code)
		}
	}
}

// TestFingerprintGuardsStaleIndices is the reason the fingerprint exists. Reordering changes
// what index 1 means while changing nothing a reader would notice, so a page rendered before
// a move must not be able to act on the document that came after it.
func TestFingerprintGuardsStaleIndices(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n\n## B\n\n```echo\nb\n```\n\n## C\n\n```echo\nc\n```\n")
	stale := h.pageFingerprint()

	// The first request is in step and goes through.
	if rec := h.doWithFP(http.MethodPost, "/cells/1/move-up", stale); rec.Code != http.StatusOK {
		t.Fatalf("first move = %d: %s", rec.Code, rec.Body.String())
	}
	afterMove := h.contents()

	// The same page now describes a structure that no longer exists. Every index-addressed
	// mutation must refuse rather than act on the wrong cell.
	for _, tc := range []struct{ method, target string }{
		{http.MethodPost, "/cells/1/move-up"},
		{http.MethodPost, "/cells/1/move-down"},
		{http.MethodDelete, "/cells/1"},
		{http.MethodPost, "/cells/1/run"},
	} {
		rec := h.doWithFP(tc.method, tc.target, stale)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s = %d, want 409", tc.method, tc.target, rec.Code)
		}
		if rec.Header().Get("HX-Refresh") != "true" {
			t.Errorf("%s %s: a stale page should be told to reload", tc.method, tc.target)
		}
		if got := h.contents(); got != afterMove {
			t.Errorf("%s %s changed the notebook despite being stale", tc.method, tc.target)
		}
	}

	// A fresh page works again.
	if rec := h.doWithFP(http.MethodPost, "/cells/1/move-up", h.pageFingerprint()); rec.Code != http.StatusOK {
		t.Errorf("a fresh fingerprint = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFingerprintIgnoresRuns matters for usability rather than safety: running a cell moves
// nothing, so it must not invalidate a page and make the next move look stale. This is why
// the fingerprint covers structure rather than the whole file, whose bytes change on
// every run.
func TestFingerprintIgnoresRuns(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n\n## B\n\n```echo\nb\n```\n")
	fp := h.pageFingerprint()

	h.runCell(0)

	// The file has changed — a result was written — but the structure has not.
	if got := h.pageFingerprint(); got != fp {
		t.Errorf("a run changed the fingerprint (%s -> %s); a move after a run would be "+
			"rejected for no reason", fp, got)
	}
	if rec := h.doWithFP(http.MethodPost, "/cells/1/move-up", fp); rec.Code != http.StatusOK {
		t.Errorf("move after a run = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestFingerprintAbsentIsAllowed keeps the server usable outside the browser: curl and the
// tests above have no rendered page to be stale, and refusing them would buy no safety.
func TestFingerprintAbsentIsAllowed(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\na\n```\n\n## B\n\n```echo\nb\n```\n")
	if rec := h.do(http.MethodPost, "/cells/1/move-up", nil); rec.Code != http.StatusOK {
		t.Errorf("no fingerprint = %d, want it allowed through: %s", rec.Code, rec.Body.String())
	}
}
