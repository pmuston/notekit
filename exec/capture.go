package exec

import "sync"

// Capture is a bounded, concurrency-safe buffer for executors that produce output
// incrementally.
//
// It exists so an executor reading from a pty or a cursor cannot exhaust memory on a
// runaway cell: bytes past the limit are counted and discarded rather than
// accumulated. The durable truncation *marker* is a format concern applied later by
// package run through [doc.Truncate] — Capture only bounds what is held in memory.
//
// Executors that build a payload in one piece have no need for this.
type Capture struct {
	limit int

	mu       sync.Mutex
	buf      []byte
	overflow int
}

// NewCapture returns a Capture holding at most limit bytes. A limit of zero or less
// means unbounded, which is only sensible in tests.
func NewCapture(limit int) *Capture {
	return &Capture{limit: limit}
}

// Write accumulates p up to the limit, discarding the remainder. It never reports a
// short write: discarding past the limit is the intended behaviour, and an executor
// should not treat it as an I/O failure.
func (c *Capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.limit <= 0 {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	room := c.limit - len(c.buf)
	if room <= 0 {
		c.overflow += len(p)
		return len(p), nil
	}
	if len(p) <= room {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	c.buf = append(c.buf, p[:room]...)
	c.overflow += len(p) - room
	return len(p), nil
}

// String returns the captured bytes.
func (c *Capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// Overflowed reports whether any bytes were discarded, and how many.
func (c *Capture) Overflowed() (bool, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overflow > 0, c.overflow
}

// Reset empties the buffer so one Capture can serve several cells.
func (c *Capture) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf, c.overflow = c.buf[:0], 0
}
