//go:build unix

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
	"github.com/pmuston/notekit/meta"
)

// testShell is the shell these tests drive. bash is the more predictable of the two and
// is present on every platform CI runs on.
const testShell = "bash"

func newSession(t *testing.T) exec.Session {
	t.Helper()
	return newSessionCap(t, doc.OutputCap)
}

func newSessionCap(t *testing.T, outputCap int) exec.Session {
	t.Helper()
	ex, err := NewShellExecutor(testShell, "dumb", outputCap)
	if err != nil {
		t.Skipf("%s unavailable: %v", testShell, err)
	}
	sess, err := ex.Open(context.Background(), t.TempDir()+"/notes.md")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// req builds a request, parsing an info string when one is given so metadata reaches the
// executor exactly as a real cell's would.
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

func TestLangIsSh(t *testing.T) {
	ex, err := NewShellExecutor(testShell, "dumb", doc.OutputCap)
	if err != nil {
		t.Skipf("%s unavailable: %v", testShell, err)
	}
	if ex.Lang() != "sh" {
		t.Errorf("Lang() = %q, want %q", ex.Lang(), "sh")
	}
}

// TestOutputIsClean is the regression for the bug only running it found: an interactive
// shell's line editor re-enabled echo and redrew a prompt before every command, so every
// cell's output was buried in echoed input and prompt padding.
func TestOutputIsClean(t *testing.T) {
	sess := newSession(t)
	got, err := runCell(t, sess, "echo hello\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, _ := got.Payload.(string)
	if body != "hello\n" {
		t.Errorf("Payload = %q, want exactly %q — no echo, no prompt, no CR", body, "hello\n")
	}
}

func TestNoCarriageReturns(t *testing.T) {
	// -onlcr stops the tty translating LF to CRLF; a stray CR would end up in the
	// durable body and show as a control character on GitHub.
	sess := newSession(t)
	got, err := runCell(t, sess, "printf 'a\\nb\\n'\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, _ := got.Payload.(string)
	if strings.Contains(body, "\r") {
		t.Errorf("Payload = %q contains a carriage return", body)
	}
	if body != "a\nb\n" {
		t.Errorf("Payload = %q, want %q", body, "a\nb\n")
	}
}

// TestStatePersistsAcrossCells is harvest R1: one session per notebook, carrying cwd,
// environment and functions between cells.
func TestStatePersistsAcrossCells(t *testing.T) {
	sess := newSession(t)

	if _, err := runCell(t, sess, "NOTEKIT_VAR=kept\nmy_fn() { echo from-a-function; }\ncd /tmp\n", "sh"); err != nil {
		t.Fatalf("first cell: %v", err)
	}

	got, err := runCell(t, sess, "echo \"$NOTEKIT_VAR\"\nmy_fn\npwd\n", "sh")
	if err != nil {
		t.Fatalf("second cell: %v", err)
	}
	body, _ := got.Payload.(string)
	for _, want := range []string{"kept", "from-a-function"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q — state did not carry", body, want)
		}
	}
	if !strings.Contains(body, "/tmp") {
		t.Errorf("body %q: the working directory did not carry", body)
	}
}

// TestExitStatusIsADomainError: a non-zero status persists as a first-class error block,
// never folded into output (§7).
func TestExitStatusIsADomainError(t *testing.T) {
	sess := newSession(t)

	_, err := runCell(t, sess, "echo before-failure\nexit_code_test() { return 42; }\nexit_code_test\n", "sh")
	var domain *exec.Error
	if !errors.As(err, &domain) {
		t.Fatalf("err = %v, want *exec.Error", err)
	}
	if domain.Status == nil || *domain.Status != 42 {
		t.Errorf("Status = %v, want 42", domain.Status)
	}
	// The body is the command's own output, which §7 wants in the error block.
	if !strings.Contains(domain.Message, "before-failure") {
		t.Errorf("Message = %q, want the command's output", domain.Message)
	}
}

func TestSuccessAfterFailureKeepsSessionUsable(t *testing.T) {
	sess := newSession(t)
	if _, err := runCell(t, sess, "false\n", "sh"); err == nil {
		t.Fatal("expected a domain error")
	}
	got, err := runCell(t, sess, "echo recovered\n", "sh")
	if err != nil {
		t.Fatalf("the session did not survive a failure: %v", err)
	}
	if body, _ := got.Payload.(string); !strings.Contains(body, "recovered") {
		t.Errorf("Payload = %q", body)
	}
}

// TestStderrInterleaves is the v1 divergence: v1 split stderr to a temp file, but §7
// requires shell stdout and stderr combined and interleaved as produced.
func TestStderrInterleaves(t *testing.T) {
	sess := newSession(t)
	got, err := runCell(t, sess, "echo one\necho two >&2\necho three\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, _ := got.Payload.(string)
	if body != "one\ntwo\nthree\n" {
		t.Errorf("Payload = %q, want interleaved %q", body, "one\ntwo\nthree\n")
	}
}

func TestTableFormats(t *testing.T) {
	sess := newSession(t)

	t.Run("csv", func(t *testing.T) {
		got, err := runCell(t, sess, "printf 'a,b\\n1,2\\n'\n", "sh {format=csv}")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Kind != kind.Table {
			t.Fatalf("Kind = %q, want %q", got.Kind, kind.Table)
		}
		p, ok := got.Payload.(kind.TablePayload)
		if !ok || p.Format != kind.CSV || p.Body != "a,b\n1,2\n" {
			t.Errorf("Payload = %#v", got.Payload)
		}
	})

	t.Run("jsonl", func(t *testing.T) {
		got, err := runCell(t, sess, "printf '{\"a\":1}\\n'\n", "sh {format=jsonl}")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		p, ok := got.Payload.(kind.TablePayload)
		if !ok || p.Format != kind.JSONL {
			t.Errorf("Payload = %#v", got.Payload)
		}
	})

	t.Run("unknown format is text", func(t *testing.T) {
		// The kit does not transcode, and `table` would reject an unknown
		// serialisation, so text is what the cell actually asked for.
		got, err := runCell(t, sess, "echo x\n", "sh {format=tsv}")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Kind != kind.Text {
			t.Errorf("Kind = %q, want %q", got.Kind, kind.Text)
		}
	})
}

// TestANSIIsNotStrippedByTheExecutor is the other v1 inversion: v1 stripped in the
// runner, but stripping is the format layer's job and doing it here would destroy the
// colour the browser renders.
func TestANSIIsNotStrippedByTheExecutor(t *testing.T) {
	sess := newSession(t)
	got, err := runCell(t, sess, "printf '\\033[31mred\\033[0m\\n'\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, _ := got.Payload.(string)
	if !strings.Contains(body, "\x1b[31m") {
		t.Errorf("Payload = %q: the escape must survive for the live view", body)
	}
	// And the format layer is what removes it.
	if strings.Contains(doc.StripANSI(body), "\x1b") {
		t.Error("doc.StripANSI should remove it")
	}
}

// TestTruncationIsReported covers the kit change M3 forced: the executor bounds its own
// capture and must say so, or the runtime would consider a short body complete.
func TestTruncationIsReported(t *testing.T) {
	sess := newSessionCap(t, 256)
	got, err := runCell(t, sess, "for i in $(seq 1 500); do echo line-$i; done\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	body, _ := got.Payload.(string)
	if len(body) > 256 {
		t.Errorf("body is %d bytes, over the 256 cap", len(body))
	}

	// And crucially the session is still in sync: reading continued past the cap, so
	// the sentinel was consumed and the next cell reads its own output.
	next, err := runCell(t, sess, "echo still-in-sync\n", "sh")
	if err != nil {
		t.Fatalf("the session desynchronised after truncation: %v", err)
	}
	if nb, _ := next.Payload.(string); nb != "still-in-sync\n" {
		t.Errorf("next cell read %q, want %q — the sentinel protocol lost sync",
			nb, "still-in-sync\n")
	}
}

// TestCancellationInterruptsAndKeepsTheSessionUsable is one of the two rules binding
// every executor. The session must survive, which means the sentinel still has to be
// consumed before Execute returns.
func TestCancellationInterruptsAndKeepsTheSessionUsable(t *testing.T) {
	sess := newSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := sess.Execute(ctx, req(t, "sleep 30\n", "sh"))
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("cancellation took %v; the command should have been interrupted", elapsed)
	}

	got, err := sess.Execute(context.Background(), req(t, "echo after-cancel\n", "sh"))
	if err != nil {
		t.Fatalf("the session did not survive cancellation: %v", err)
	}
	if body, _ := got.Payload.(string); body != "after-cancel\n" {
		t.Errorf("Payload = %q, want %q — the session lost sync", body, "after-cancel\n")
	}
}

func TestAlreadyCancelledContext(t *testing.T) {
	sess := newSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sess.Execute(ctx, req(t, "echo x\n", "sh")); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestCloseUnblocksAHungCommand is why Close does not take the mutex: a Close that
// waited would deadlock behind a stuck Execute, and Close runs on the Ctrl-C path.
func TestCloseUnblocksAHungCommand(t *testing.T) {
	ex, err := NewShellExecutor(testShell, "dumb", doc.OutputCap)
	if err != nil {
		t.Skipf("%s unavailable: %v", testShell, err)
	}
	sess, err := ex.Open(context.Background(), t.TempDir()+"/notes.md")
	if err != nil {
		t.Fatal(err)
	}

	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		// No cancellation: only Close can end this.
		_, _ = sess.Execute(context.Background(), exec.Request{Source: "sleep 60\n"})
	}()
	time.Sleep(300 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- sess.Close(context.Background()) }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked behind a hung command; it must not take the mutex")
	}

	select {
	case <-execDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the hung Execute did not unwind after Close")
	}
}

func TestClosedSessionRefusesWork(t *testing.T) {
	sess := newSession(t)
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent: run's Shutdown and a tool's defer may both call it.
	if err := sess.Close(context.Background()); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if _, err := sess.Execute(context.Background(), exec.Request{Source: "echo x\n"}); err == nil {
		t.Error("Execute on a closed session = nil error, want error")
	}
}

func TestMultiLineAndQuotingSurvive(t *testing.T) {
	sess := newSession(t)
	tests := []struct{ name, source, want string }{
		{"single quotes", "echo 'it'\\''s fine'\n", "it's fine\n"},
		{"double quotes with expansion", "X=v\necho \"got $X\"\n", "got v\n"},
		{"heredoc", "cat <<'EOF'\nline one\nline two\nEOF\n", "line one\nline two\n"},
		{"pipeline", "printf 'b\\na\\n' | sort\n", "a\nb\n"},
		{"no trailing newline in source", "echo tail", "tail\n"},
		{"backticks in output", "echo '```'\n", "```\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runCell(t, sess, tt.source, "sh")
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if body, _ := got.Payload.(string); body != tt.want {
				t.Errorf("Payload = %q, want %q", body, tt.want)
			}
		})
	}
}

// TestSentinelCannotBeForged: the sentinel is random per session, so a cell echoing a
// plausible marker cannot fake an exit status or truncate its own output.
func TestSentinelCannotBeForged(t *testing.T) {
	sess := newSession(t)
	got, err := runCell(t, sess, "echo '__NOTEKIT_END_deadbeef__:0'\necho after\n", "sh")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, _ := got.Payload.(string)
	if !strings.Contains(body, "after") {
		t.Errorf("Payload = %q: a forged marker ended the read early", body)
	}
}

func TestNewShellExecutorValidation(t *testing.T) {
	tests := []struct {
		name  string
		shell string
		cap   int
	}{
		{"unsupported shell", "fish", doc.OutputCap},
		{"empty shell", "", doc.OutputCap},
		{"absolute path rejected", "/bin/bash", doc.OutputCap},
		{"zero cap", testShell, 0},
		{"negative cap", testShell, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewShellExecutor(tt.shell, "dumb", tt.cap); err == nil {
				t.Error("want error")
			}
		})
	}
}

func TestSentinelsAreDistinctPerSession(t *testing.T) {
	a, b := newSession(t).(*shellSession), newSession(t).(*shellSession)
	if a.sentinel == b.sentinel {
		t.Error("two sessions share a sentinel; a forged marker would work across them")
	}
	if !strings.HasPrefix(a.sentinel, "__NOTEKIT_END_") || len(a.sentinel) < 30 {
		t.Errorf("sentinel = %q", a.sentinel)
	}
}
