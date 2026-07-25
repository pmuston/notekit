package doc

import "strings"

// SlugMaxLen bounds the slug so that `<slug>--<id>.<ext>` stays well inside
// filesystem name limits (§5.2).
const SlugMaxLen = 60

// Slug derives a cell's cosmetic name from its heading text (§5.2).
//
// The slug carries no identity — it is recomputed on every parse, exists only to
// make sidecar filenames legible, and may be empty or shared by two cells. None of
// those is an error. Identity is the stored id (§5.1).
func Slug(heading string) string {
	var b strings.Builder
	b.Grow(len(heading))
	pendingDash := false

	for _, r := range strings.ToLower(heading) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
			continue
		}
		// Every maximal run outside [a-z0-9] collapses to a single '-', and a
		// run that turns out to be trailing collapses to nothing.
		pendingDash = true
	}

	s := b.String()
	if len(s) > SlugMaxLen {
		s = strings.TrimRight(s[:SlugMaxLen], "-")
	}
	return s
}
