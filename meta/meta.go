// Package meta implements the notekit info-string metadata grammar.
//
// Implements notekit-format-spec.md §9: parsing an info string into an ordered
// key/value structure that preserves each entry's original source text,
// duplicate-key rejection, and canonical serialisation with reserved keys first.
//
// It also provides append-only insertion of a single entry — the mechanism by
// which a cell's id is added to a hand-authored source fence without reformatting
// it (§5.1). Existing entries keep their text, spacing, and order byte-for-byte;
// this is a deliberate exception to the reserved-keys-first canonical ordering,
// which governs tool-written result blocks only.
//
// This package assigns no meaning to any key. It does not know that "output" is a
// result tag or that "id" is an identity, and it does not hold the reserved-key
// orders from §6/§7 — callers pass those to [Format]. Keeping semantics out is
// what lets the same grammar serve source fences, result blocks, and provenance
// comments.
//
// # Layering of errors
//
// Parse reports malformed metadata and duplicate keys as errors. Whether such an
// error means "this construct is prose" (§3, conservative parsing) or "fail loud"
// (§9, a tool error) is the caller's decision, because it depends on whether the
// tool was asked to run or write the block. This package only reports.
package meta

import (
	"fmt"
	"strings"
)

// Entry is one key/value pair from an info string.
//
// A valueless key is a boolean flag, true (§9); Flag is set and Value is empty.
// Quoted records whether the value was written in quotes, so that a tool
// reproducing a block can match the source's style — the format permits either
// form wherever quoting is not required.
type Entry struct {
	Key    string
	Value  string
	Flag   bool
	Quoted bool

	raw string // exact source text, key through value; empty if constructed

	// start and end bound raw within the info string, so one entry can be
	// replaced without disturbing another byte ([Info.Set]). Both are zero for a
	// constructed Entry, which is why Set works from Info's own slice rather than
	// from an Entry handed back by [Info.Get].
	start, end int
}

// Raw returns the entry's exact source text, or "" if the Entry was constructed
// rather than parsed.
func (e Entry) Raw() string { return e.raw }

// Info is a parsed fence info string.
//
// [Info.String] returns the original bytes verbatim, which is what makes §10's
// round-trip guarantee hold for blocks a tool did not write.
type Info struct {
	tag     string
	entries []Entry
	raw     string

	tagEnd       int // index just past the tag
	metaOpen     int // index of '{', or -1 when there is no metadata block
	lastEntryEnd int // index just past the final entry's raw text
}

// Tag returns the info string's tag — a language tag, "output", or "error" (§9).
// It is empty for a fence with no info string.
func (i *Info) Tag() string { return i.tag }

// Entries returns the metadata entries in source order.
func (i *Info) Entries() []Entry { return i.entries }

// HasMeta reports whether the info string carried a metadata block.
func (i *Info) HasMeta() bool { return i.metaOpen >= 0 }

// Get returns the entry for key.
func (i *Info) Get(key string) (Entry, bool) {
	for _, e := range i.entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// String returns the info string's original bytes, unchanged.
func (i *Info) String() string { return i.raw }

// SyntaxError reports metadata that does not match the §9 grammar.
type SyntaxError struct {
	Offset int // byte offset within the info string
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("info string: %s at offset %d", e.Msg, e.Offset)
}

// DuplicateKeyError reports a key appearing more than once in one info string,
// which §9 makes a tool error rather than something to resolve silently.
type DuplicateKeyError struct {
	Key    string
	Offset int
}

func (e *DuplicateKeyError) Error() string {
	return fmt.Sprintf("info string: duplicate key %q at offset %d", e.Key, e.Offset)
}

// Tag returns an info string's tag without validating the metadata that follows,
// using the same delimiters as [Parse]: the tag ends at whitespace or '{'.
//
// Callers that need the tag even when the metadata is malformed use this — a fence
// with a broken info string still has a structural role (§4.3), and refusing to
// report its tag would turn a metadata error into a cell-detection error.
func Tag(s string) string {
	p := 0
	for p < len(s) && isSpace(s[p]) {
		p++
	}
	start := p
	for p < len(s) && !isSpace(s[p]) && s[p] != '{' {
		p++
	}
	return s[start:p]
}

// Parse parses a fence info string.
//
// The empty string yields an Info with an empty tag and no entries, which is how
// an untagged fence appears (§4.3).
func Parse(s string) (*Info, error) {
	i := &Info{raw: s, metaOpen: -1, lastEntryEnd: -1}

	p := 0
	for p < len(s) && isSpace(s[p]) {
		p++
	}
	tagStart := p
	for p < len(s) && !isSpace(s[p]) && s[p] != '{' {
		p++
	}
	i.tag = s[tagStart:p]
	i.tagEnd = p

	// The grammar is `tag [ SP metadata ]`: a brace touching the tag is a
	// separator error, not a tag character. Rejecting it catches `sh{a=1}`
	// rather than silently accepting "sh{a=1}" as a tag nothing will match.
	if p < len(s) && s[p] == '{' {
		return nil, &SyntaxError{Offset: p, Msg: "metadata must be separated from the tag by a space"}
	}
	for p < len(s) && isSpace(s[p]) {
		p++
	}
	if p == len(s) {
		return i, nil
	}
	if s[p] != '{' {
		return nil, &SyntaxError{Offset: p, Msg: "expected '{' or end of info string"}
	}

	i.metaOpen = p
	p++ // past '{'

	seen := make(map[string]bool)
	for {
		for p < len(s) && isSpace(s[p]) {
			p++
		}
		if p == len(s) {
			return nil, &SyntaxError{Offset: p, Msg: "unterminated metadata block"}
		}
		if s[p] == '}' {
			// `metadata = "{" entry ( "," entry )* "}"` requires at least one
			// entry, so `{}` is malformed rather than an empty set.
			return nil, &SyntaxError{Offset: p, Msg: "empty metadata block"}
		}

		entryStart := p
		key, next, err := parseKey(s, p)
		if err != nil {
			return nil, err
		}
		p = next
		if seen[key] {
			return nil, &DuplicateKeyError{Key: key, Offset: entryStart}
		}
		seen[key] = true

		e := Entry{Key: key, Flag: true}
		end := p

		q := p
		for q < len(s) && isSpace(s[q]) {
			q++
		}
		if q < len(s) && s[q] == '=' {
			q++
			for q < len(s) && isSpace(s[q]) {
				q++
			}
			val, quoted, next, err := parseValue(s, q)
			if err != nil {
				return nil, err
			}
			e.Value, e.Quoted, e.Flag = val, quoted, false
			p, end = next, next
		}
		e.raw = s[entryStart:end]
		e.start, e.end = entryStart, end
		i.entries = append(i.entries, e)
		i.lastEntryEnd = end

		for p < len(s) && isSpace(s[p]) {
			p++
		}
		if p == len(s) {
			return nil, &SyntaxError{Offset: p, Msg: "unterminated metadata block"}
		}
		switch s[p] {
		case ',':
			p++
		case '}':
			p++
			for p < len(s) && isSpace(s[p]) {
				p++
			}
			if p != len(s) {
				return nil, &SyntaxError{Offset: p, Msg: "trailing content after metadata block"}
			}
			return i, nil
		default:
			return nil, &SyntaxError{Offset: p, Msg: "expected ',' or '}'"}
		}
	}
}

// parseKey reads `[a-z] [a-z0-9_-]*` at p.
func parseKey(s string, p int) (string, int, error) {
	if p >= len(s) || s[p] < 'a' || s[p] > 'z' {
		return "", 0, &SyntaxError{Offset: p, Msg: "key must begin with a lower-case letter"}
	}
	q := p + 1
	for q < len(s) && isKeyChar(s[q]) {
		q++
	}
	return s[p:q], q, nil
}

// parseValue reads a bare or quoted value at p, returning the decoded value.
func parseValue(s string, p int) (val string, quoted bool, next int, err error) {
	if p < len(s) && s[p] == '"' {
		var b strings.Builder
		q := p + 1
		for q < len(s) {
			switch s[q] {
			case '"':
				return b.String(), true, q + 1, nil
			case '\\':
				// Only \" and \\ are escapes (§9). Anything else is
				// malformed: keeping escaping a bijection is what lets
				// decode and re-encode agree byte for byte.
				if q+1 >= len(s) {
					return "", false, 0, &SyntaxError{Offset: q, Msg: "unterminated escape"}
				}
				if s[q+1] != '"' && s[q+1] != '\\' {
					return "", false, 0, &SyntaxError{Offset: q, Msg: `only \" and \\ may be escaped`}
				}
				b.WriteByte(s[q+1])
				q += 2
			default:
				b.WriteByte(s[q])
				q++
			}
		}
		return "", false, 0, &SyntaxError{Offset: p, Msg: "unterminated quoted value"}
	}

	q := p
	for q < len(s) && !isBareTerm(s[q]) {
		q++
	}
	if q == p {
		return "", false, 0, &SyntaxError{Offset: p, Msg: "empty bare value (use \"\" for an empty string)"}
	}
	return s[p:q], false, q, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// isBareTerm reports whether c ends a bare value: `bare` excludes whitespace and
// , = { } " (§9).
func isBareTerm(c byte) bool {
	switch c {
	case ',', '=', '{', '}', '"':
		return true
	}
	return isSpace(c)
}
