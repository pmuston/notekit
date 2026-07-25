package doc

import (
	"crypto/rand"
	"fmt"
)

// KeyID is the reserved source-fence key holding a cell's durable identity (§5.1).
const KeyID = "id"

// IDLen is the number of characters in an id, and idAlphabet is lower-case base32
// (RFC 4648). Eight characters give 40 bits, which makes collision irrelevant at
// any notebook size while staying short enough not to clutter a source fence.
const IDLen = 8

const idAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// NewID returns a fresh identity token.
//
// Callers pass ids to [Cell.AssignID] rather than this package generating them
// internally, which is what keeps golden tests deterministic: a test supplies its
// own fixed tokens and never has to stub a generator.
func NewID() (string, error) {
	buf := make([]byte, IDLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("notekit: generating cell id: %w", err)
	}
	out := make([]byte, IDLen)
	for i, b := range buf {
		out[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(out), nil
}

// ValidID reports whether s is a well-formed id: exactly [IDLen] characters of
// lower-case base32.
func ValidID(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIDChar(s[i]) {
			return false
		}
	}
	return true
}

func isIDChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '2' && c <= '7'
}

// DuplicateIDError reports the same id on two cells, which §5.1 makes a tool
// error. It is only reachable by copy-pasting a cell that already carries one; the
// fix is to delete the token from the copy and let a run assign a fresh one.
type DuplicateIDError struct {
	ID       string
	Headings [2]string
}

func (e *DuplicateIDError) Error() string {
	return fmt.Sprintf("duplicate cell id %q on %q and %q (delete one and re-run that cell to have a fresh id assigned)",
		e.ID, e.Headings[0], e.Headings[1])
}
