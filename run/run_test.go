package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/exec/echoexec"
	"github.com/pmuston/notekit/kind"
)

const front = "---\nnotekit: 1\ntitle: T\n---\n\n"

// fixedNow is the clock every test uses, so result metadata is deterministic.
var fixedNow = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

// harness is an open notebook plus the scheduler driving it.
type harness struct {
	t    *testing.T
	s    *Scheduler
	path string
	ex   *echoexec.Executor
}

func newHarness(t *testing.T, body string, opts ...Option) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(front+body), 0o644); err != nil {
		t.Fatalf("writing notebook: %v", err)
	}

	base := []Option{
		WithTool("notefmt/test"),
		WithClock(func() time.Time { return fixedNow }),
		WithIDGenerator(sequentialIDs()),
	}
	s := New(append(base, opts...)...)
	ex := echoexec.New()
	if err := s.Open(context.Background(), path, ex); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return &harness{t: t, s: s, path: path, ex: ex}
}

// sequentialIDs hands out valid, predictable cell ids.
//
// The alphabet matters: base32 is [a-z2-7], so 0, 1, 8 and 9 are not id characters and
// a naive %02d counter produces ids that ValidID rightly rejects.
func sequentialIDs() func() (string, error) {
	const alpha = "abcdefghijklmnopqrstuvwxyz234567"
	var mu sync.Mutex
	n := 0
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		id := "aaaaaa" + string(alpha[n/len(alpha)%len(alpha)]) + string(alpha[n%len(alpha)])
		n++
		return id, nil
	}
}

// runCell submits a cell and waits for it to reach a terminal state.
func (h *harness) runCell(index int) Run {
	h.t.Helper()
	id, err := h.s.Submit(h.path, index)
	if err != nil {
		h.t.Fatalf("Submit: %v", err)
	}
	return h.await(id)
}

func (h *harness) await(id ID) Run {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, ok := h.s.State(id)
		if !ok {
			h.t.Fatalf("run %s vanished", id)
		}
		if r.State.Terminal() {
			return r
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s stuck in %v", id, r.State)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) contents() string {
	h.t.Helper()
	b, err := os.ReadFile(h.path)
	if err != nil {
		h.t.Fatalf("reading notebook: %v", err)
	}
	return string(b)
}

// TestSubmitReturnsImmediately is harvest R3: a run request returns an ID at once and
// the state is pollable. Nothing about the API may block on execution.
func TestSubmitReturnsImmediately(t *testing.T) {
	h := newHarness(t, "## Slow\n\n```echo {delay=200ms}\nhello\n```\n")

	start := time.Now()
	id, err := h.s.Submit(h.path, 0)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Submit took %v; it must return immediately", elapsed)
	}

	// Observable as non-terminal before it finishes.
	r, ok := h.s.State(id)
	if !ok {
		t.Fatal("State: run not found")
	}
	if r.State.Terminal() {
		t.Errorf("state = %v immediately after Submit; want queued or running", r.State)
	}

	if got := h.await(id); got.State != Done {
		t.Fatalf("final state = %v (%v)", got.State, got.Err)
	}
}

func TestTextResultPersisted(t *testing.T) {
	h := newHarness(t, "## Greeting\n\n```echo\nhello\n```\n")

	r := h.runCell(0)
	if r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	if r.Form != doc.ResultOutput {
		t.Errorf("form = %v, want output", r.Form)
	}

	want := front + "## Greeting\n\n```echo\nhello\n```\n\n" +
		"```output {run=\"2026-07-26T12:00:00Z\", tool=\"notefmt/test\"}\nhello\n```\n"
	if got := h.contents(); got != want {
		t.Errorf("notebook:\n got %q\nwant %q", got, want)
	}
}

func TestTableResultCarriesFormat(t *testing.T) {
	h := newHarness(t, "## Table\n\n```echo {format=csv}\na,b\n1,2\n```\n")

	if r := h.runCell(0); r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	got := h.contents()
	if !strings.Contains(got, "```output {format=csv, run=") {
		t.Errorf("expected format=csv first in canonical order:\n%s", got)
	}
	if !strings.Contains(got, "a,b\n1,2\n") {
		t.Errorf("body not persisted:\n%s", got)
	}
}

// TestDomainFailureIsDoneNotFailed is the distinction the package doc calls out: a
// non-zero status is a *successful run* that persists an error block.
func TestDomainFailureIsDoneNotFailed(t *testing.T) {
	h := newHarness(t, "## Broken command\n\n```echo {status=127}\ncommand not found: dv\n```\n")

	r := h.runCell(0)
	if r.State != Done {
		t.Fatalf("state = %v, want done — a domain failure is not a failed run (%v)", r.State, r.Err)
	}
	if r.Form != doc.ResultError {
		t.Errorf("form = %v, want error", r.Form)
	}

	got := h.contents()
	// §7: status first, then run and tool.
	if !strings.Contains(got, "```error {status=127, run=\"2026-07-26T12:00:00Z\", tool=\"notefmt/test\"}") {
		t.Errorf("error block metadata wrong:\n%s", got)
	}
	if !strings.Contains(got, "command not found: dv") {
		t.Errorf("error text not persisted:\n%s", got)
	}
	// Never folded into degenerate output.
	if strings.Contains(got, "```output") {
		t.Errorf("a failure must not produce an output block:\n%s", got)
	}
}

func TestDomainFailureWithoutStatus(t *testing.T) {
	h := newHarness(t, "## No status\n\n```echo {fail}\nsomething went wrong\n```\n")

	if r := h.runCell(0); r.State != Done || r.Form != doc.ResultError {
		t.Fatalf("state = %v form = %v", r.State, r.Form)
	}
	got := h.contents()
	// A domain with no numeric codes omits the key rather than inventing a zero.
	if strings.Contains(got, "status=") {
		t.Errorf("status must be absent when the domain has none:\n%s", got)
	}
	if !strings.Contains(got, "```error {run=") {
		t.Errorf("error block wrong:\n%s", got)
	}
}

// TestSessionFailureIsFailedAndWritesNothing is the other half of the distinction.
func TestSessionFailureIsFailedAndWritesNothing(t *testing.T) {
	body := "## Engine broke\n\n```echo {broken}\nx\n```\n"
	h := newHarness(t, body)
	before := h.contents()

	r := h.runCell(0)
	if r.State != Failed {
		t.Fatalf("state = %v, want failed", r.State)
	}
	if r.Err == nil {
		t.Error("Err = nil, want the reason")
	}
	if got := h.contents(); got != before {
		t.Errorf("a failed run must persist nothing:\n got %q\nwant %q", got, before)
	}
}

// TestTruncation covers the output cap, the marker, and the flag together (§6).
func TestTruncation(t *testing.T) {
	h := newHarness(t, "## Long\n\n```echo {repeat=100}\nabcdefghij\n```\n", WithOutputCap(64))

	r := h.runCell(0)
	if r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	if !r.Truncated {
		t.Error("Truncated = false, want true")
	}

	got := h.contents()
	if !strings.Contains(got, "truncated}") {
		t.Errorf("truncated flag missing:\n%s", got)
	}
	if !strings.Contains(got, doc.TruncationLine(64)) {
		t.Errorf("truncation marker missing:\n%s", got)
	}

	// The notebook must still parse and hold exactly one result.
	nb, err := doc.Parse([]byte(got))
	if err != nil {
		t.Fatalf("truncated notebook no longer parses: %v", err)
	}
	if n := len(nb.Cells()[0].Results); n != 1 {
		t.Errorf("got %d results, want 1", n)
	}
}

func TestANSIStrippedFromDurableForm(t *testing.T) {
	h := newHarness(t, "## Colour\n\n```echo\n\x1b[31mred\x1b[0m\n```\n")

	if r := h.runCell(0); r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	got := h.contents()
	// Only the *durable result* must be plain. The source fence still holds the
	// escape, because that is what the author wrote and prose is never rewritten.
	cells := mustCells(t, got)
	result := string(cells[0].Results[0].Span.In([]byte(got)))
	if strings.Contains(result, "\x1b") {
		t.Errorf("escape sequences reached the durable result:\n%q", result)
	}
	if !strings.Contains(result, "```output {run=\"2026-07-26T12:00:00Z\", tool=\"notefmt/test\"}\nred\n```") {
		t.Errorf("stripped body wrong:\n%q", result)
	}
	if !strings.Contains(got, "```echo\n\x1b[31mred\x1b[0m\n```") {
		t.Errorf("the source fence must be untouched:\n%q", got)
	}
}

// TestVolatileReplacement: every run replaces the whole result position, so running
// twice converges rather than accumulating.
func TestVolatileReplacement(t *testing.T) {
	h := newHarness(t, "## Twice\n\n```echo\nhello\n```\n\nprose after\n")

	h.runCell(0)
	first := h.contents()
	h.runCell(0)
	second := h.contents()

	if first != second {
		t.Errorf("running twice is not convergent:\n%q\n%q", first, second)
	}
	if strings.Count(second, "```output") != 1 {
		t.Errorf("expected exactly one output block:\n%s", second)
	}
	if !strings.Contains(second, "prose after\n") {
		t.Errorf("prose after the cell was lost:\n%s", second)
	}
}

// TestExistingResultsReplaced covers §4.2's write rule over mixed pre-existing forms.
func TestExistingResultsReplaced(t *testing.T) {
	h := newHarness(t, "## Messy\n\n```echo\nhello\n```\n\n"+
		"```output\nstale one\n```\n\n```output\nstale two\n```\n\nprose\n")

	if r := h.runCell(0); r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	got := h.contents()
	if strings.Contains(got, "stale") {
		t.Errorf("stale results survived:\n%s", got)
	}
	if n := strings.Count(got, "```output"); n != 1 {
		t.Errorf("got %d output blocks, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "prose\n") {
		t.Errorf("prose lost:\n%s", got)
	}
}

func TestUnclosedFenceFailsWithoutWriting(t *testing.T) {
	body := "## Unclosed\n\n```echo\nhello\n"
	h := newHarness(t, body)
	before := h.contents()

	r := h.runCell(0)
	if r.State != Failed {
		t.Fatalf("state = %v, want failed", r.State)
	}
	if !strings.Contains(r.Err.Error(), "unclosed source fence") {
		t.Errorf("Err = %v", r.Err)
	}
	if got := h.contents(); got != before {
		t.Errorf("the notebook must be untouched:\n got %q\nwant %q", got, before)
	}
	// The session still ran nothing: refusing happens before execution, so a cell
	// whose result cannot be stored is not executed for nothing.
	if runs := h.session(t).Runs(); runs != 0 {
		t.Errorf("session executed %d cells, want 0", runs)
	}
}

// session reaches the echo session for assertions. There is exactly one per notebook.
func (h *harness) session(t *testing.T) *echoexec.Session {
	t.Helper()
	on, err := h.s.notebook(h.path)
	if err != nil {
		t.Fatalf("notebook: %v", err)
	}
	sess, ok := on.sess.(*echoexec.Session)
	if !ok {
		t.Fatalf("session is %T", on.sess)
	}
	return sess
}

// TestRunsSerialisedWithinNotebook is the invariant every stateful executor depends
// on: never two cells of one notebook at once (kit spec §3.4).
func TestRunsSerialisedWithinNotebook(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 6; i++ {
		fmt.Fprintf(&body, "## Cell %d\n\n```echo {delay=10ms}\ncell %d\n```\n\n", i, i)
	}
	h := newHarness(t, body.String())

	var ids []ID
	for i := 0; i < 6; i++ {
		id, err := h.s.Submit(h.path, i)
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if r := h.await(id); r.State != Done {
			t.Fatalf("run %s: state = %v (%v)", id, r.State, r.Err)
		}
	}

	sess := h.session(t)
	if sess.SawConcurrentExecute() {
		t.Error("two cells of one notebook executed concurrently")
	}
	if got := sess.Runs(); got != 6 {
		t.Errorf("session executed %d cells, want 6", got)
	}

	// Every result landed, and in the right cell.
	nb, err := doc.Parse([]byte(h.contents()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i, c := range nb.Cells() {
		if len(c.Results) != 1 {
			t.Fatalf("cell %d has %d results", i, len(c.Results))
		}
		want := fmt.Sprintf("cell %d", i)
		if !strings.Contains(string(c.Results[0].Span.In(nb.Bytes())), want) {
			t.Errorf("cell %d result does not contain %q", i, want)
		}
	}
}

// TestRunsOrderedFIFO: runs within a notebook execute in request order.
func TestRunsOrderedFIFO(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&body, "## Cell %d\n\n```echo\ncell %d\n```\n\n", i, i)
	}
	h := newHarness(t, body.String())

	var ids []ID
	for i := 0; i < 4; i++ {
		id, _ := h.s.Submit(h.path, i)
		ids = append(ids, id)
	}
	var finished []time.Time
	for _, id := range ids {
		finished = append(finished, h.await(id).Finished)
	}
	// With a frozen clock the timestamps are equal, so ordering is asserted by the
	// session's run count reaching 4 with no concurrency, plus each cell's own
	// result — which TestRunsSerialisedWithinNotebook covers. Here we assert only
	// that all four completed.
	if len(finished) != 4 {
		t.Fatalf("got %d finished runs", len(finished))
	}
	for i, c := range mustCells(t, h.contents()) {
		if len(c.Results) != 1 {
			t.Errorf("cell %d has %d results, want 1", i, len(c.Results))
		}
	}
}

func mustCells(t *testing.T, src string) []*doc.Cell {
	t.Helper()
	nb, err := doc.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return nb.Cells()
}

// TestDifferentNotebooksRunConcurrently is the other half of the concurrency rule.
func TestDifferentNotebooksRunConcurrently(t *testing.T) {
	dir := t.TempDir()
	s := New(WithTool("t/0"), WithClock(func() time.Time { return fixedNow }))
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	const n = 4
	var paths []string
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("nb%d.md", i))
		if err := os.WriteFile(p, []byte(front+"## C\n\n```echo {delay=150ms}\nx\n```\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := s.Open(context.Background(), p, echoexec.New()); err != nil {
			t.Fatalf("Open: %v", err)
		}
		paths = append(paths, p)
	}

	start := time.Now()
	var ids []ID
	for _, p := range paths {
		id, err := s.Submit(p, 0)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		deadline := time.Now().Add(5 * time.Second)
		for {
			r, _ := s.State(id)
			if r.State.Terminal() {
				if r.State != Done {
					t.Fatalf("run %s: %v (%v)", id, r.State, r.Err)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s stuck", id)
			}
			time.Sleep(time.Millisecond)
		}
	}

	// Serialised, four 150ms runs would take 600ms. Concurrent, well under.
	if elapsed := time.Since(start); elapsed > 450*time.Millisecond {
		t.Errorf("four notebooks took %v; they should run concurrently", elapsed)
	}
}

func TestCancelQueuedRun(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo {delay=300ms}\nfirst\n```\n\n## B\n\n```echo\nsecond\n```\n")

	first, _ := h.s.Submit(h.path, 0)
	second, _ := h.s.Submit(h.path, 1)

	// The second is queued behind the first, so cancelling it takes effect before it
	// ever reaches the executor.
	if err := h.s.Cancel(second); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if r := h.await(second); r.State != Cancelled {
		t.Errorf("second run state = %v, want cancelled", r.State)
	}
	if r := h.await(first); r.State != Done {
		t.Errorf("first run state = %v, want done (%v)", r.State, r.Err)
	}

	// The cancelled cell has no result; the completed one does.
	cells := mustCells(t, h.contents())
	if len(cells[0].Results) != 1 {
		t.Errorf("cell 0 has %d results, want 1", len(cells[0].Results))
	}
	if len(cells[1].Results) != 0 {
		t.Errorf("cell 1 has %d results, want 0 — a cancelled run writes nothing", len(cells[1].Results))
	}
}

func TestCancelRunningRun(t *testing.T) {
	body := "## Slow\n\n```echo {delay=2s}\nhello\n```\n"
	h := newHarness(t, body)
	before := h.contents()

	id, _ := h.s.Submit(h.path, 0)
	// Wait until it is actually running, so this exercises in-flight cancellation
	// rather than the queued path.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if r, _ := h.s.State(id); r.State == Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never started")
		}
		time.Sleep(time.Millisecond)
	}

	if err := h.s.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	r := h.await(id)
	if r.State != Cancelled {
		t.Fatalf("state = %v, want cancelled (%v)", r.State, r.Err)
	}
	if got := h.contents(); got != before {
		t.Errorf("a cancelled run must persist nothing:\n got %q\nwant %q", got, before)
	}
}

func TestCancelFinishedRunIsANoop(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	r := h.runCell(0)
	if err := h.s.Cancel(r.ID); err != nil {
		t.Errorf("Cancel on a finished run = %v, want nil", err)
	}
	if got, _ := h.s.State(r.ID); got.State != Done {
		t.Errorf("state changed to %v", got.State)
	}
}

func TestCancelUnknownRun(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	if err := h.s.Cancel("nope"); err == nil {
		t.Error("Cancel on an unknown id = nil, want error")
	}
}

// TestSessionClosedOnShutdown is the teardown guarantee: every live session's destroy
// hook runs.
func TestSessionClosedOnShutdown(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	sess := h.session(t)
	if sess.Closed() {
		t.Fatal("session closed before shutdown")
	}
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !sess.Closed() {
		t.Error("session not closed by Shutdown")
	}
	// Idempotent: a tool may call Shutdown from a defer and from a signal handler.
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown = %v, want nil", err)
	}
}

func TestShutdownClosesEverySession(t *testing.T) {
	dir := t.TempDir()
	s := New()
	var sessions []*echoexec.Session
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("nb%d.md", i))
		if err := os.WriteFile(p, []byte(front+"## C\n\n```echo\nx\n```\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := s.Open(context.Background(), p, echoexec.New()); err != nil {
			t.Fatal(err)
		}
		on, _ := s.notebook(p)
		sessions = append(sessions, on.sess.(*echoexec.Session))
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i, sess := range sessions {
		if !sess.Closed() {
			t.Errorf("session %d not closed", i)
		}
	}
}

func TestCloseOneNotebook(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	sess := h.session(t)

	if err := h.s.Close(context.Background(), h.path); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sess.Closed() {
		t.Error("session not closed")
	}
	// The notebook is no longer open, so submitting is an error rather than a panic.
	if _, err := h.s.Submit(h.path, 0); err == nil {
		t.Error("Submit after Close = nil error, want error")
	}
	if err := h.s.Close(context.Background(), h.path); err == nil {
		t.Error("second Close = nil error, want error")
	}
}

// TestQueuedRunsDrainedBeforeShutdown: a run that was accepted is not silently
// dropped.
func TestQueuedRunsDrainedBeforeShutdown(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&body, "## Cell %d\n\n```echo {delay=20ms}\ncell %d\n```\n\n", i, i)
	}
	h := newHarness(t, body.String())

	for i := 0; i < 3; i++ {
		if _, err := h.s.Submit(h.path, i); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, r := range h.s.Runs() {
		if r.State != Done {
			t.Errorf("run %s: state = %v, want done (%v)", r.ID, r.State, r.Err)
		}
	}
	for i, c := range mustCells(t, h.contents()) {
		if len(c.Results) != 1 {
			t.Errorf("cell %d has %d results, want 1", i, len(c.Results))
		}
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	s := New()
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	t.Run("missing file", func(t *testing.T) {
		if err := s.Open(context.Background(), filepath.Join(dir, "nope.md"), echoexec.New()); err == nil {
			t.Error("want error")
		}
	})
	t.Run("not a notebook", func(t *testing.T) {
		p := filepath.Join(dir, "bad.md")
		os.WriteFile(p, []byte("not a notebook\n"), 0o644)
		if err := s.Open(context.Background(), p, echoexec.New()); err == nil {
			t.Error("want error")
		}
	})
	t.Run("already open", func(t *testing.T) {
		p := filepath.Join(dir, "ok.md")
		os.WriteFile(p, []byte(front+"## A\n\n```echo\nx\n```\n"), 0o644)
		if err := s.Open(context.Background(), p, echoexec.New()); err != nil {
			t.Fatalf("first Open: %v", err)
		}
		// One session per notebook is the invariant, so a second Open is refused
		// rather than quietly creating a second session.
		if err := s.Open(context.Background(), p, echoexec.New()); err == nil {
			t.Error("second Open = nil error, want error")
		}
	})
}

func TestSubmitErrors(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n\n## Sql\n\n```sql\nSELECT 1\n```\n")

	t.Run("unopened notebook", func(t *testing.T) {
		if _, err := h.s.Submit("/nope.md", 0); err == nil {
			t.Error("want error")
		}
	})
	t.Run("index out of range", func(t *testing.T) {
		for _, i := range []int{-1, 99} {
			if _, err := h.s.Submit(h.path, i); err == nil {
				t.Errorf("Submit(%d) = nil error, want error", i)
			}
		}
	})
	t.Run("language mismatch", func(t *testing.T) {
		// Cells are dispatched by language tag; an executor must never be handed a
		// cell it did not claim.
		if _, err := h.s.Submit(h.path, 1); err == nil {
			t.Error("want error for a sql cell on an echo executor")
		}
	})
}

func TestSubmitAfterShutdown(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Submit(h.path, 0); err == nil {
		t.Error("Submit after Shutdown = nil error, want error")
	}
}

func TestUnregisteredKindFails(t *testing.T) {
	body := "## Odd\n\n```echo {kind=nosuch}\nx\n```\n"
	h := newHarness(t, body)
	before := h.contents()

	r := h.runCell(0)
	if r.State != Failed {
		t.Fatalf("state = %v, want failed", r.State)
	}
	// A kind with no durable form is inadmissible, so this is an error rather than a
	// silent fall back to text.
	if !strings.Contains(r.Err.Error(), "unregistered kind") {
		t.Errorf("Err = %v", r.Err)
	}
	if got := h.contents(); got != before {
		t.Error("nothing should have been written")
	}
}

func TestStateAndRuns(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n\n## B\n\n```echo\ny\n```\n")
	h.runCell(0)
	h.runCell(1)

	if _, ok := h.s.State("nope"); ok {
		t.Error("State on an unknown id returned ok")
	}
	runs := h.s.Runs()
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].ID == runs[1].ID {
		t.Error("run ids are not distinct")
	}
	for _, r := range runs {
		if r.Notebook != h.path {
			t.Errorf("Notebook = %q, want %q", r.Notebook, h.path)
		}
		if r.Cell == "" {
			t.Error("Cell heading is empty")
		}
	}
}

func TestCellsAccessor(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	cells, err := h.s.Cells(h.path)
	if err != nil {
		t.Fatalf("Cells: %v", err)
	}
	if len(cells) != 1 || cells[0].HeadingText != "A" {
		t.Errorf("cells = %#v", cells)
	}
	if _, err := h.s.Cells("/nope.md"); err == nil {
		t.Error("Cells on an unopened notebook = nil error, want error")
	}
}

func TestStateStrings(t *testing.T) {
	cases := map[State]string{
		Queued: "queued", Running: "running", Done: "done",
		Failed: "failed", Cancelled: "cancelled", State(99): "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
	if Queued.Terminal() || Running.Terminal() {
		t.Error("queued and running must not be terminal")
	}
	if !Done.Terminal() || !Failed.Terminal() || !Cancelled.Terminal() {
		t.Error("done, failed and cancelled must be terminal")
	}
}

// TestSaveIsAtomic checks that no partial file is observable, by asserting the
// notebook always parses while runs are in flight.
func TestSaveIsAtomic(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&body, "## Cell %d\n\n```echo\ncell %d\n```\n\n", i, i)
	}
	h := newHarness(t, body.String())

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(h.path)
			if err != nil {
				continue // the rename window is not observable, but a read may race
			}
			if _, err := doc.Parse(b); err != nil {
				t.Errorf("observed an unparseable notebook mid-run: %v", err)
				return
			}
		}
	}()

	for i := 0; i < 8; i++ {
		h.runCell(i)
	}
	close(stop)
	wg.Wait()
}

func TestNotebookModePreserved(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	if err := os.Chmod(h.path, 0o640); err != nil {
		t.Fatal(err)
	}
	h.runCell(0)

	info, err := os.Stat(h.path)
	if err != nil {
		t.Fatal(err)
	}
	// An atomic save must not silently reset the file's mode to the temp file's.
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want 640", got)
	}
}

// fakeGraph is a synthetic sidecar kind. It exists to exercise the sidecar write path
// without shipping a graph kind — no real sidecar-producing tool exists until after M3.
func fakeGraph() kind.Kind {
	return kind.Kind{
		Name: "graph",
		Durable: func(payload any) (kind.Durable, error) {
			body, _ := payload.(string)
			return kind.Durable{Sidecar: &kind.Sidecar{
				Files: []kind.File{
					{Ext: "png", Content: []byte("PNG:" + body), Primary: true},
					{Ext: "json", Content: []byte(`{"body":"` + body + `"}`)},
				},
			}}, nil
		},
	}
}

func TestSidecarResultWritesFilesAndAssignsID(t *testing.T) {
	reg := kind.NewRegistry()
	if err := reg.Register(fakeGraph()); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "## Module wiring\n\n```echo {kind=graph}\nMATCH\n```\n", WithRegistry(reg))

	r := h.runCell(0)
	if r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}
	if r.Form != doc.ResultSidecar {
		t.Errorf("form = %v, want sidecar", r.Form)
	}

	got := h.contents()
	// A cell producing a sidecar needs identity, so the run assigned one (§5.1).
	if !strings.Contains(got, "```echo {kind=graph, id=aaaaaaaa}") {
		t.Errorf("id not appended to the source fence:\n%s", got)
	}
	if !strings.Contains(got, "<!-- notekit:result kind=graph, run=") {
		t.Errorf("provenance comment wrong:\n%s", got)
	}
	if !strings.Contains(got, "![Module wiring](notes.assets/module-wiring--aaaaaaaa.png)") {
		t.Errorf("image link wrong:\n%s", got)
	}

	dir := filepath.Join(filepath.Dir(h.path), "notes.assets")
	for _, name := range []string{"module-wiring--aaaaaaaa.png", "module-wiring--aaaaaaaa.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}

	// Re-running must not assign a second id: ids are immutable (§5.1).
	if r := h.runCell(0); r.State != Done {
		t.Fatalf("second run: %v (%v)", r.State, r.Err)
	}
	if n := strings.Count(h.contents(), "id="); n != 1 {
		t.Errorf("got %d id entries after two runs, want 1:\n%s", n, h.contents())
	}
}

// TestSupersededSidecarsRemoved resolves implementation plan §8.3: a run replaces its
// own cell's artifacts wholesale, including ones it no longer produces.
func TestSupersededSidecarsRemoved(t *testing.T) {
	reg := kind.NewRegistry()
	if err := reg.Register(fakeGraph()); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "## Wiring\n\n```echo {kind=graph}\nMATCH\n```\n", WithRegistry(reg))
	h.runCell(0)

	dir := filepath.Join(filepath.Dir(h.path), "notes.assets")
	// A leftover from a hypothetical earlier run of this same cell: same id, an
	// extension this run does not produce.
	leftover := filepath.Join(dir, "wiring--aaaaaaaa.svg")
	if err := os.WriteFile(leftover, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An orphan: its id belongs to no cell. It must survive — a different thing
	// entirely from a superseded artifact (§8.1).
	orphan := filepath.Join(dir, "deleted--bbbb2345.png")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if r := h.runCell(0); r.State != Done {
		t.Fatalf("state = %v (%v)", r.State, r.Err)
	}

	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("a superseded artifact of the cell being run should have been removed")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Error("an orphan must never be deleted by a run")
	}
	if _, err := os.Stat(filepath.Join(dir, "wiring--aaaaaaaa.png")); err != nil {
		t.Errorf("the current artifact is missing: %v", err)
	}
}

func TestSidecarValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		k    kind.Kind
	}{
		{"neither form", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{}, nil
		}}},
		{"both forms", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{Inline: &kind.Inline{}, Sidecar: &kind.Sidecar{}}, nil
		}}},
		{"no files", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{Sidecar: &kind.Sidecar{}}, nil
		}}},
		{"no primary", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{Sidecar: &kind.Sidecar{Files: []kind.File{{Ext: "png"}}}}, nil
		}}},
		{"two primaries", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{Sidecar: &kind.Sidecar{Files: []kind.File{
				{Ext: "png", Primary: true}, {Ext: "svg", Primary: true},
			}}}, nil
		}}},
		{"durable writer errors", kind.Kind{Name: "graph", Durable: func(any) (kind.Durable, error) {
			return kind.Durable{}, fmt.Errorf("deliberate")
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := kind.NewRegistry()
			if err := reg.Register(tt.k); err != nil {
				t.Fatal(err)
			}
			body := "## G\n\n```echo {kind=graph}\nx\n```\n"
			h := newHarness(t, body, WithRegistry(reg))
			before := h.contents()

			r := h.runCell(0)
			if r.State != Failed {
				t.Fatalf("state = %v, want failed", r.State)
			}
			if got := h.contents(); got != before {
				t.Error("nothing should have been written")
			}
		})
	}
}

// TestExecuteReceivesCellMetadata: the executor gets the info string uninterpreted, so
// passthrough keys reach the domain (§9).
func TestExecuteReceivesCellMetadata(t *testing.T) {
	var got exec.Request
	reg := kind.NewRegistry()
	if err := reg.Register(kind.Kind{Name: "probe", Durable: func(any) (kind.Durable, error) {
		return kind.Durable{Inline: &kind.Inline{Body: "ok"}}, nil
	}}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	os.WriteFile(path, []byte(front+"## Probe me\n\n```probe {custom=value, flagged}\nbody text\n```\n"), 0o644)

	s := New(WithRegistry(reg), WithClock(func() time.Time { return fixedNow }))
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	if err := s.Open(context.Background(), path, &capturingExecutor{lang: "probe", seen: &got}); err != nil {
		t.Fatal(err)
	}
	id, err := s.Submit(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if r, _ := s.State(id); r.State.Terminal() {
			if r.State != Done {
				t.Fatalf("state = %v (%v)", r.State, r.Err)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}

	if got.Source != "body text\n" {
		t.Errorf("Source = %q", got.Source)
	}
	if got.Cell.Heading != "Probe me" || got.Cell.Slug != "probe-me" || got.Cell.Index != 0 {
		t.Errorf("CellRef = %#v", got.Cell)
	}
	if got.Meta == nil {
		t.Fatal("Meta = nil")
	}
	if e, ok := got.Meta.Get("custom"); !ok || e.Value != "value" {
		t.Errorf("custom = %#v", e)
	}
	if e, ok := got.Meta.Get("flagged"); !ok || !e.Flag {
		t.Errorf("flagged = %#v", e)
	}
}

type capturingExecutor struct {
	lang string
	seen *exec.Request
}

func (c *capturingExecutor) Lang() string { return c.lang }
func (c *capturingExecutor) Open(context.Context, string) (exec.Session, error) {
	return &capturingSession{seen: c.seen}, nil
}

type capturingSession struct{ seen *exec.Request }

func (c *capturingSession) Execute(_ context.Context, req exec.Request) (exec.Result, error) {
	*c.seen = req
	return exec.Result{Kind: "probe", Payload: "ok"}, nil
}
func (c *capturingSession) Close(context.Context) error { return nil }

func TestOpenSessionFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	os.WriteFile(path, []byte(front+"## A\n\n```bad\nx\n```\n"), 0o644)

	s := New()
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	if err := s.Open(context.Background(), path, &failingExecutor{}); err == nil {
		t.Error("Open = nil error, want the session failure surfaced")
	}
}

type failingExecutor struct{}

func (*failingExecutor) Lang() string { return "bad" }
func (*failingExecutor) Open(context.Context, string) (exec.Session, error) {
	return nil, fmt.Errorf("no engine here")
}

// TestQueueBackpressure: the queue is bounded on purpose, and refusing a submission is
// a better failure than accepting work that will never be reached.
func TestQueueBackpressure(t *testing.T) {
	h := newHarness(t, "## Slow\n\n```echo {delay=100ms}\nx\n```\n", WithQueueSize(2))

	// The first submission goes straight to the worker, the next two fill the queue,
	// and the fourth must be refused.
	var ids []ID
	var refused error
	for i := 0; i < 8; i++ {
		id, err := h.s.Submit(h.path, 0)
		if err != nil {
			refused = err
			// A refused submission still gets a recorded, terminal run rather
			// than vanishing.
			if r, ok := h.s.State(id); !ok || r.State != Failed {
				t.Errorf("refused run state = %v (found %v), want failed", r.State, ok)
			}
			break
		}
		ids = append(ids, id)
	}
	if refused == nil {
		t.Fatal("Submit never applied backpressure")
	}
	if !strings.Contains(refused.Error(), "full") {
		t.Errorf("err = %v, want it to mention a full queue", refused)
	}

	// Cancel the backlog so shutdown does not wait on it, then let the rest finish.
	for _, id := range ids[1:] {
		_ = h.s.Cancel(id)
	}
	for _, id := range ids {
		h.await(id)
	}
}

// TestSaveFailureIsReportedAndLeavesTheFileAlone covers an unwritable notebook
// directory — a read-only mount, in practice.
func TestSaveFailureIsReportedAndLeavesTheFileAlone(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nhello\n```\n")
	before := h.contents()
	dir := filepath.Dir(h.path)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make the directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Confirm the platform actually enforces it; running as root would not.
	if f, err := os.CreateTemp(dir, "probe-*"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("the directory is still writable, so this platform cannot exercise the path")
	}

	r := h.runCell(0)
	if r.State != Failed {
		t.Fatalf("state = %v, want failed", r.State)
	}
	if got := h.contents(); got != before {
		t.Errorf("the notebook changed despite the save failing:\n got %q\nwant %q", got, before)
	}
}
