//go:build unix

package main

import (
	"context"
	"errors"
	"os"
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

// requireExecutor builds an executor for sh, or ends the test.
//
// A developer without zsh installed should still be able to run the suite, so a missing
// shell is a skip locally. In CI it is a failure: the interactive-stdin bug shipped behind
// a green tick that had only ever exercised bash, and a skip would reproduce exactly that
// blindness — the run would pass while proving nothing about the shell in question. CI is
// set by GitHub Actions, so this needs no workflow configuration to stay strict.
func requireExecutor(t *testing.T, sh string) *ShellExecutor {
	t.Helper()
	ex, err := NewShellExecutor(sh, "dumb", doc.OutputCap)
	if err == nil {
		return ex
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s unavailable: %v\n\n"+
			"CI must exercise every shell in supportedShells. Install it in the "+
			"workflow rather than letting this skip.", sh, err)
	}
	t.Skipf("%s unavailable: %v", sh, err)
	return nil
}

func newSession(t *testing.T) exec.Session {
	t.Helper()
	return newSessionCap(t, doc.OutputCap)
}

func newSessionCap(t *testing.T, outputCap int) exec.Session {
	t.Helper()
	ex, err := NewShellExecutor(testShell, "dumb", outputCap)
	if err != nil {
		requireExecutor(t, testShell) // reports the skip-or-fail policy
		t.Fatalf("%s unavailable: %v", testShell, err)
	}
	sess, err := ex.Open(context.Background(), exec.Notebook{Path: t.TempDir() + "/notes.md"})
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
	ex := requireExecutor(t, testShell)
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
	ex := requireExecutor(t, testShell)
	sess, err := ex.Open(context.Background(), exec.Notebook{Path: t.TempDir() + "/notes.md"})
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

// supportedShells is every shell NewShellExecutor accepts. The matrix test below runs
// against all of them, which is the structural fix for how the zsh bug survived: the rest
// of this file uses one shell, and for a long time that shell was bash — which tolerates
// a configuration zsh does not.
var supportedShells = []string{"bash", "zsh"}

// TestEveryShellIsQuietAndUsable is the regression the earlier test matrix could not
// catch. It asserts the properties that broke under zsh, for every shell clinote claims
// to support.
//
// The bug: a shell decides it is interactive when *stdin is a terminal*, whatever flags it
// was given. An interactive zsh sourced .zshrc, set a prompt, and ran its line editor —
// which re-enabled echo after `stty -echo`, redrew the prompt before every command, and
// emitted bracketed-paste escapes, all of it landing in the captured output. bash under the
// same arrangement stayed quiet, so a single-shell suite reported success.
func TestEveryShellIsQuietAndUsable(t *testing.T) {
	for _, sh := range supportedShells {
		t.Run(sh, func(t *testing.T) {
			ex := requireExecutor(t, sh)
			sess, err := ex.Open(context.Background(), exec.Notebook{Path: t.TempDir() + "/n.md"})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = sess.Close(context.Background()) })

			run := func(source string) string {
				t.Helper()
				got, err := sess.Execute(context.Background(), exec.Request{Source: source})
				if err != nil {
					t.Fatalf("Execute(%q): %v", source, err)
				}
				body, _ := got.Payload.(string)
				return body
			}

			// Exactly the output, with nothing the shell added.
			if body := run("echo hello\n"); body != "hello\n" {
				t.Errorf("Payload = %q, want exactly %q", body, "hello\n")
			}

			// The specific artefacts the interactive shell produced. Each is checked by
			// name so a failure says which mechanism came back.
			body := run("echo marker\n")
			for _, junk := range []struct{ what, seq string }{
				{"a prompt", "%"},
				{"bracketed paste", "\x1b[?2004"},
				{"a carriage return", "\r"},
				{"echoed input", "echo marker"},
				{"the sentinel", "__NOTEKIT_END_"},
			} {
				if strings.Contains(body, junk.seq) {
					t.Errorf("output contains %s (%q): %q", junk.what, junk.seq, body)
				}
			}

			// State carries: one long-lived shell (harvest R1).
			run("NOTEKIT_MATRIX=kept\ncd /tmp\n")
			if body := run("echo \"$NOTEKIT_MATRIX\"\npwd\n"); !strings.Contains(body, "kept") ||
				!strings.Contains(body, "/tmp") {
				t.Errorf("state did not carry: %q", body)
			}

			// stdout and stderr interleave as produced (§7).
			if body := run("echo one\necho two >&2\necho three\n"); body != "one\ntwo\nthree\n" {
				t.Errorf("Payload = %q, want interleaved", body)
			}

			// A non-zero status is a domain failure carrying the code.
			_, err = sess.Execute(context.Background(), exec.Request{Source: "( exit 42 )\n"})
			var domain *exec.Error
			if !errors.As(err, &domain) {
				t.Fatalf("err = %v, want *exec.Error", err)
			}
			if domain.Status == nil || *domain.Status != 42 {
				t.Errorf("Status = %v, want 42", domain.Status)
			}

			// Cancellation interrupts the command and leaves the session usable. This is
			// why the setup traps INT rather than enabling job control: without a
			// handler the interrupt would kill the shell along with the command.
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(300 * time.Millisecond)
				cancel()
			}()
			if _, err := sess.Execute(ctx, exec.Request{Source: "sleep 30\n"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if body := run("echo survived\n"); body != "survived\n" {
				t.Errorf("the session did not survive cancellation: %q", body)
			}

			// Colour still reaches the executor raw, which is the reason for a pty at
			// all — stripping is the format layer's job.
			if body := run("printf '\\033[31mred\\033[0m\\n'\n"); !strings.Contains(body, "\x1b[31m") {
				t.Errorf("the escape did not survive: %q", body)
			}
		})
	}
}

// TestShellIsNonInteractive pins the mechanism rather than only its symptoms: if a future
// change makes the shell interactive again, this fails first and says why.
func TestShellIsNonInteractive(t *testing.T) {
	for _, sh := range supportedShells {
		t.Run(sh, func(t *testing.T) {
			ex := requireExecutor(t, sh)
			sess, err := ex.Open(context.Background(), exec.Notebook{Path: t.TempDir() + "/n.md"})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = sess.Close(context.Background()) })

			got, err := sess.Execute(context.Background(), exec.Request{
				Source: "case \"$-\" in *i*) echo INTERACTIVE ;; *) echo NON-INTERACTIVE ;; esac\n",
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if body, _ := got.Payload.(string); body != "NON-INTERACTIVE\n" {
				t.Errorf("shell reports %q; a terminal on stdin is what makes it "+
					"interactive, so stdin must stay a pipe", body)
			}

			// And stdout is still a terminal, which is what makes colour possible.
			got, err = sess.Execute(context.Background(), exec.Request{
				Source: "[ -t 1 ] && echo TTY || echo NOT-TTY\n",
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if body, _ := got.Payload.(string); body != "TTY\n" {
				t.Errorf("stdout reports %q, want TTY — programs detect colour from it", body)
			}
		})
	}
}

// TestDrainConsumesPendingBytesWithoutWaiting pins both halves of drain's contract, and
// the old implementation failed a different half on each platform: on darwin a pty master
// rejects SetReadDeadline outright, so drain returned having consumed nothing, and on linux
// the deadline was accepted and then ignored — creack/pty's ioctls go through File.Fd(),
// which restores blocking mode — so the read waited forever and hung session init for the
// whole test timeout in CI.
//
// The assertion is behavioural rather than an FIONREAD count, because what actually
// matters is that one cell's late output is never attributed to the next cell. A test that
// only checked "drain returns promptly" would have passed on darwin while draining nothing.
func TestDrainConsumesPendingBytesWithoutWaiting(t *testing.T) {
	sess := newSession(t)
	s := sess.(*shellSession)

	// Bytes with no sentinel following them, exactly as a process backgrounded by a
	// previous cell would produce. Nothing consumes these.
	if _, err := s.in.Write([]byte("printf 'stray-bytes\\n'\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // let them land on the master

	// Execute drains first, so a hanging drain hangs here — which is what CI saw.
	done := make(chan struct{})
	var got exec.Result
	var execErr error
	go func() {
		got, execErr = s.Execute(context.Background(), exec.Request{Source: "echo after-drain\n"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute hung: drain must bound itself on the descriptor (O_NONBLOCK), " +
			"not on a read deadline that a pty master may reject or ignore")
	}
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	// Exactly the new cell's output. "stray-bytes" here means drain returned without
	// consuming anything, which is what darwin did.
	body, _ := got.Payload.(string)
	if strings.Contains(body, "stray-bytes") {
		t.Errorf("the previous cell's late output leaked into this cell: %q", body)
	}
	if body != "after-drain\n" {
		t.Errorf("Payload = %q, want exactly %q", body, "after-drain\n")
	}

	// Still usable, so drain restored the descriptor's flags. Leaving O_NONBLOCK on would
	// break readUntilSentinel, which blocks deliberately.
	got, err := s.Execute(context.Background(), exec.Request{Source: "echo still-fine\n"})
	if err != nil {
		t.Fatalf("Execute after drain: %v", err)
	}
	if body, _ := got.Payload.(string); body != "still-fine\n" {
		t.Errorf("Payload = %q, want %q", body, "still-fine\n")
	}
}
