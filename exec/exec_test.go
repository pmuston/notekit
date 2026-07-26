package exec

import (
	"strings"
	"sync"
	"testing"
)

func TestErrorMessages(t *testing.T) {
	// A status is included when the domain has one, and omitted when it does not —
	// a nil Status is normal, not missing information (§7).
	if got, want := NewError(127, "command not found: %s", "dv").Error(),
		"command not found: dv (status 127)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got, want := NewErrorNoStatus("syntax error at %d", 4).Error(),
		"syntax error at 4"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrorStatusRoundTrip(t *testing.T) {
	e := NewError(1, "boom")
	if e.Status == nil || *e.Status != 1 {
		t.Errorf("Status = %v, want 1", e.Status)
	}
	if NewErrorNoStatus("boom").Status != nil {
		t.Error("Status should be nil for a domain with no numeric codes")
	}
}

func TestCaptureBounds(t *testing.T) {
	c := NewCapture(10)

	n, err := c.Write([]byte("abcde"))
	if n != 5 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := c.String(); got != "abcde" {
		t.Errorf("String() = %q", got)
	}
	if over, count := c.Overflowed(); over || count != 0 {
		t.Errorf("Overflowed() = %v, %d; want false, 0", over, count)
	}

	// A write that straddles the limit keeps the part that fits and counts the rest.
	// It must not report a short write: discarding is the intended behaviour, and an
	// executor should not treat it as an I/O failure.
	n, err = c.Write([]byte("fghijklmno"))
	if n != 10 || err != nil {
		t.Fatalf("straddling Write = %d, %v; want 10, nil", n, err)
	}
	if got := c.String(); got != "abcdefghij" {
		t.Errorf("String() = %q, want %q", got, "abcdefghij")
	}
	over, count := c.Overflowed()
	if !over || count != 5 {
		t.Errorf("Overflowed() = %v, %d; want true, 5", over, count)
	}

	// Writes once full are counted and dropped.
	if n, err := c.Write([]byte("pqr")); n != 3 || err != nil {
		t.Fatalf("full Write = %d, %v", n, err)
	}
	if got := c.String(); got != "abcdefghij" {
		t.Errorf("String() = %q, want the buffer unchanged", got)
	}
	if _, count := c.Overflowed(); count != 8 {
		t.Errorf("overflow count = %d, want 8", count)
	}
}

func TestCaptureUnbounded(t *testing.T) {
	c := NewCapture(0)
	if _, err := c.Write([]byte(strings.Repeat("x", 1000))); err != nil {
		t.Fatal(err)
	}
	if got := len(c.String()); got != 1000 {
		t.Errorf("length = %d, want 1000", got)
	}
	if over, _ := c.Overflowed(); over {
		t.Error("an unbounded capture cannot overflow")
	}
}

func TestCaptureReset(t *testing.T) {
	c := NewCapture(4)
	c.Write([]byte("abcdef"))
	c.Reset()
	if got := c.String(); got != "" {
		t.Errorf("String() after Reset = %q", got)
	}
	if over, count := c.Overflowed(); over || count != 0 {
		t.Errorf("Overflowed() after Reset = %v, %d", over, count)
	}
	// Reusable for the next cell.
	c.Write([]byte("xy"))
	if got := c.String(); got != "xy" {
		t.Errorf("String() = %q, want %q", got, "xy")
	}
}

// TestCaptureConcurrentWrites: an executor pumping stdout and stderr from two
// goroutines is the normal case, so Capture must be safe for it.
func TestCaptureConcurrentWrites(t *testing.T) {
	c := NewCapture(0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()
	if got := len(c.String()); got != 800 {
		t.Errorf("length = %d, want 800", got)
	}
}

// TestCaptureBoundedConcurrently checks the limit holds under concurrency, which is
// the property that keeps a runaway cell from exhausting memory.
func TestCaptureBoundedConcurrently(t *testing.T) {
	const limit = 100
	c := NewCapture(limit)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Write([]byte("xyz"))
			}
		}()
	}
	wg.Wait()
	if got := len(c.String()); got != limit {
		t.Errorf("length = %d, want exactly the limit %d", got, limit)
	}
	if over, count := c.Overflowed(); !over || count != 8*100*3-limit {
		t.Errorf("Overflowed() = %v, %d; want true, %d", over, count, 8*100*3-limit)
	}
}

func TestNotebookFrontValue(t *testing.T) {
	nb := Notebook{
		Path:  "/notes.md",
		Title: "T",
		Front: map[string]string{"sqlnote-db": "./x.db", "blank": ""},
	}

	if got := nb.FrontValue("sqlnote-db", ":memory:"); got != "./x.db" {
		t.Errorf("FrontValue(present) = %q", got)
	}
	if got := nb.FrontValue("absent", ":memory:"); got != ":memory:" {
		t.Errorf("FrontValue(absent) = %q, want the default", got)
	}
	// Absent and empty are treated alike on purpose: `sqlnote-db:` with nothing after
	// it says no more than omitting the key, and distinguishing them would assign
	// meaning the format does not.
	if got := nb.FrontValue("blank", ":memory:"); got != ":memory:" {
		t.Errorf("FrontValue(empty) = %q, want the default", got)
	}
	// A nil map is the normal case for a notebook with no passthrough keys.
	if got := (Notebook{}).FrontValue("any", "fallback"); got != "fallback" {
		t.Errorf("FrontValue on a nil map = %q", got)
	}
}
