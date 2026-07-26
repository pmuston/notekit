package doc

import (
	"bytes"
	"fmt"
	"strings"
)

// Scaffold returns the bytes of a brand-new notebook: front matter carrying `notekit: 1`
// and an optional title, then one cell.
//
// The cell is not decoration. A notebook's engine is derived from the info-string tags its
// cells carry (§2.1) — nothing in front matter names a runner — and an empty notebook has
// no tags to derive from. A starter cell is what keeps every notebook's engine knowable
// from the moment it exists, so `new` and that decision are two halves of one thing.
//
// Nothing else is invented: no prose, no placeholder result. That is §10 f applied to a
// whole file rather than to a single cell.
func Scaffold(title string, first NewCell) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("---\n")
	b.WriteString("notekit: 1\n")
	if title != "" {
		if strings.ContainsAny(title, "\r\n") {
			return nil, fmt.Errorf("notekit: title must not contain a newline")
		}
		b.WriteString("title: " + yamlScalar(title) + "\n")
	}
	// A blank line after the front matter, matching how a notebook is written by hand.
	b.WriteString("---\n\n")

	// Parse and go through AppendCell rather than formatting the cell here: the fence
	// writer already handles heading levels, info strings and fence-length safety, and it
	// is the path the corpus and fuzz targets cover.
	nb, err := Parse(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("notekit: scaffolding: %w", err)
	}
	edit, err := nb.AppendCell(first)
	if err != nil {
		return nil, fmt.Errorf("notekit: scaffolding: %w", err)
	}
	return nb.Apply(edit)
}

// yamlScalar renders s as a YAML scalar, quoting only when a plain scalar would not read
// back as the same string. Quoting unconditionally would be safe but would put quotes
// around the ordinary titles that make up almost every notebook.
func yamlScalar(s string) string {
	plain := s != "" &&
		s == strings.TrimSpace(s) &&
		!strings.Contains(s, ": ") &&
		!strings.HasSuffix(s, ":") &&
		!strings.Contains(s, " #") &&
		!strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") &&
		!isYAMLReserved(s)
	if plain {
		return s
	}
	// Double quotes with the two escapes YAML requires inside them.
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// isYAMLReserved reports whether a plain scalar would be read as something other than a
// string — a bool, null, or a number.
func isYAMLReserved(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	// A value that parses as a number is not a string either. Checking the shape is
	// enough; the exact numeric grammar does not matter, only that it is ambiguous.
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("0123456789+-._eExXaAbBcCdDfF", r)
	}) < 0
}
