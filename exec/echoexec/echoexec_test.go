package echoexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
	"github.com/pmuston/notekit/meta"
)

// request builds a Request with the given info string's metadata, which is how the
// executor is steered — the format assigns no meaning to source-fence keys, so
// interpreting them is exactly an executor's job (§9).
func request(t *testing.T, source, info string) exec.Request {
	t.Helper()
	var m *meta.Info
	if info != "" {
		var err error
		m, err = meta.Parse(info)
		if err != nil {
			t.Fatalf("parsing %q: %v", info, err)
		}
	}
	return exec.Request{Source: source, Meta: m, Cell: exec.CellRef{Heading: "H", Slug: "h"}}
}

func session(t *testing.T) *Session {
	t.Helper()
	s, err := New().Open(context.Background(), exec.Notebook{Path: "notes.md"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sess, ok := s.(*Session)
	if !ok {
		t.Fatalf("session is %T", s)
	}
	return sess
}

func TestLangAndOpen(t *testing.T) {
	if got := New().Lang(); got != Lang {
		t.Errorf("Lang() = %q, want %q", got, Lang)
	}
	sess := session(t)
	if sess.Closed() {
		t.Error("a fresh session reports itself closed")
	}
	if sess.Runs() != 0 {
		t.Error("a fresh session has a non-zero run count")
	}
	if !strings.Contains(sess.String(), "notes.md") {
		t.Errorf("String() = %q, want the notebook path", sess.String())
	}
}

func TestText(t *testing.T) {
	sess := session(t)
	got, err := sess.Execute(context.Background(), request(t, "hello\n", "echo"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Kind != kind.Text {
		t.Errorf("Kind = %q, want %q", got.Kind, kind.Text)
	}
	if got.Payload != "hello\n" {
		t.Errorf("Payload = %#v", got.Payload)
	}
	if sess.Runs() != 1 {
		t.Errorf("Runs() = %d, want 1", sess.Runs())
	}
}

func TestTable(t *testing.T) {
	for _, format := range []string{kind.CSV, kind.JSONL} {
		t.Run(format, func(t *testing.T) {
			sess := session(t)
			got, err := sess.Execute(context.Background(),
				request(t, "a,b\n", "echo {format="+format+"}"))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got.Kind != kind.Table {
				t.Errorf("Kind = %q, want %q", got.Kind, kind.Table)
			}
			p, ok := got.Payload.(kind.TablePayload)
			if !ok {
				t.Fatalf("Payload is %T, want kind.TablePayload", got.Payload)
			}
			if p.Format != format || p.Body != "a,b\n" {
				t.Errorf("Payload = %#v", p)
			}
		})
	}
}

// TestUnknownFormatFallsBackToText: an unrecognised format is not this executor's
// concern to validate — `table` would reject it, and `text` is what the cell actually
// asked for by not naming a table serialisation.
func TestUnknownFormatFallsBackToText(t *testing.T) {
	sess := session(t)
	got, err := sess.Execute(context.Background(), request(t, "x\n", "echo {format=tsv}"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Kind != kind.Text {
		t.Errorf("Kind = %q, want %q", got.Kind, kind.Text)
	}
}

func TestDomainFailures(t *testing.T) {
	tests := []struct {
		name       string
		info       string
		source     string
		wantMsg    string
		wantStatus *int
	}{
		{name: "fail flag", info: "echo {fail}", source: "went wrong\n", wantMsg: "went wrong"},
		{name: "fail with empty source", info: "echo {fail}", source: "", wantMsg: "deliberate failure"},
		{name: "status implies failure", info: "echo {status=127}", source: "not found\n",
			wantMsg: "not found", wantStatus: intp(127)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := session(t)
			_, err := sess.Execute(context.Background(), request(t, tt.source, tt.info))
			var domain *exec.Error
			if !errors.As(err, &domain) {
				t.Fatalf("err = %v, want *exec.Error", err)
			}
			if !strings.Contains(domain.Message, tt.wantMsg) {
				t.Errorf("Message = %q, want it to contain %q", domain.Message, tt.wantMsg)
			}
			switch {
			case tt.wantStatus == nil && domain.Status != nil:
				t.Errorf("Status = %d, want nil", *domain.Status)
			case tt.wantStatus != nil && (domain.Status == nil || *domain.Status != *tt.wantStatus):
				t.Errorf("Status = %v, want %d", domain.Status, *tt.wantStatus)
			}
		})
	}
}

func intp(n int) *int { return &n }

// TestSessionFailureIsNotADomainError: a broken engine is not an *exec.Error, which is
// what lets the runtime tell "the run could not complete" from "the cell reported a
// failure".
func TestSessionFailureIsNotADomainError(t *testing.T) {
	sess := session(t)
	_, err := sess.Execute(context.Background(), request(t, "x\n", "echo {broken}"))
	if err == nil {
		t.Fatal("want an error")
	}
	var domain *exec.Error
	if errors.As(err, &domain) {
		t.Error("a session failure must not be an *exec.Error")
	}
}

func TestRepeat(t *testing.T) {
	sess := session(t)
	got, err := sess.Execute(context.Background(), request(t, "ab", "echo {repeat=3}"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Payload != "ababab" {
		t.Errorf("Payload = %#v", got.Payload)
	}
}

func TestUnregisteredKindIsPassedThrough(t *testing.T) {
	// The executor names a kind; whether it is registered is the runtime's business.
	sess := session(t)
	got, err := sess.Execute(context.Background(), request(t, "x\n", "echo {kind=nosuch}"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Kind != "nosuch" {
		t.Errorf("Kind = %q", got.Kind)
	}
}

// TestCancellationHonoured is one of the two rules binding every executor.
func TestCancellationHonoured(t *testing.T) {
	t.Run("cancelled before execute", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		sess := session(t)
		if _, err := sess.Execute(ctx, request(t, "x\n", "echo")); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("cancelled during a delay", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		sess := session(t)
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		_, err := sess.Execute(ctx, request(t, "x\n", "echo {delay=5s}"))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		// It must return promptly rather than sleeping out the delay.
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %v; cancellation must be prompt", elapsed)
		}
	})
	t.Run("session usable after cancellation", func(t *testing.T) {
		sess := session(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = sess.Execute(ctx, request(t, "x\n", "echo"))
		// The contract requires the session to stay usable, or to report itself
		// unusable — this one stays usable.
		if _, err := sess.Execute(context.Background(), request(t, "y\n", "echo")); err != nil {
			t.Errorf("session unusable after a cancelled run: %v", err)
		}
	})
}

func TestClosedSessionRefusesWork(t *testing.T) {
	sess := session(t)
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sess.Closed() {
		t.Error("Closed() = false after Close")
	}
	if _, err := sess.Execute(context.Background(), request(t, "x\n", "echo")); err == nil {
		t.Error("Execute on a closed session = nil error, want error")
	}
}

func TestNilMetadataIsHandled(t *testing.T) {
	// A fence with a malformed info string yields nil Meta, which must not panic.
	sess := session(t)
	got, err := sess.Execute(context.Background(), exec.Request{Source: "x\n"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Kind != kind.Text {
		t.Errorf("Kind = %q, want %q", got.Kind, kind.Text)
	}
}

func TestMalformedOptionValuesIgnored(t *testing.T) {
	// A value that does not parse is ignored rather than failing the run: the format
	// assigns these keys no meaning, so an executor decides how forgiving to be.
	sess := session(t)
	got, err := sess.Execute(context.Background(),
		request(t, "x\n", "echo {repeat=lots, delay=soon, status=maybe}"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Payload != "x\n" {
		t.Errorf("Payload = %#v, want the source unchanged", got.Payload)
	}
}

func TestConcurrencyDetectorDetects(t *testing.T) {
	// The detector exists so run's tests can assert serialisation; check it can
	// actually fire, or the assertion would be vacuous.
	sess := session(t)
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = sess.Execute(context.Background(), request(t, "x\n", "echo {delay=60ms}"))
		}()
	}
	<-done
	<-done
	if !sess.SawConcurrentExecute() {
		t.Error("the concurrency detector did not fire on two overlapping executions")
	}
}
