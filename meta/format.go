package meta

import (
	"fmt"
	"strings"
)

// Insert returns the info string with one entry appended, leaving every existing
// byte in place (§9, append-only insertion). This is how a cell's id reaches a
// hand-authored source fence without reformatting it (§5.1).
//
// The entry goes immediately after the final existing entry, *before* any
// whitespace that preceded the closing brace — so `sh {format=csv }` becomes
// `sh {format=csv, id=… }`. That keeps the edit strictly additive (no byte is
// removed) while still reading tidily, which resolves the question the
// implementation plan raised as §8.6.
//
// A fence with no metadata block gains one directly after its tag, so trailing
// whitespace on the original line survives too.
func (i *Info) Insert(key, value string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	if _, ok := i.Get(key); ok {
		return "", &DuplicateKeyError{Key: key}
	}
	if i.tag == "" {
		return "", fmt.Errorf("info string: cannot add metadata to an untagged fence")
	}

	entry := formatEntry(Entry{Key: key, Value: value})
	if !i.HasMeta() {
		return i.raw[:i.tagEnd] + " {" + entry + "}" + i.raw[i.tagEnd:], nil
	}
	return i.raw[:i.lastEntryEnd] + ", " + entry + i.raw[i.lastEntryEnd:], nil
}

// Format serialises an info string in canonical form (§9): a single space between
// tag and metadata, single spaces after commas, reservedOrder's keys first in the
// order given, then every remaining entry in the order supplied.
//
// Callers own reservedOrder because this package holds no key semantics — the
// §6/§7 orders belong to whichever package writes result blocks.
//
// Canonical form applies only to blocks a tool writes from scratch. Never use it
// to rewrite a block the tool did not write (§10); use [Info.Insert] for the one
// permitted in-place edit.
func Format(tag string, entries []Entry, reservedOrder []string) (string, error) {
	if err := validateTag(tag); err != nil {
		return "", err
	}
	if tag == "" {
		if len(entries) > 0 {
			return "", fmt.Errorf("info string: metadata requires a tag")
		}
		return "", nil
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if err := validateKey(e.Key); err != nil {
			return "", err
		}
		if seen[e.Key] {
			return "", &DuplicateKeyError{Key: e.Key}
		}
		seen[e.Key] = true
	}

	ordered := make([]Entry, 0, len(entries))
	used := make([]bool, len(entries))
	for _, k := range reservedOrder {
		for idx, e := range entries {
			if !used[idx] && e.Key == k {
				ordered = append(ordered, e)
				used[idx] = true
				break
			}
		}
	}
	for idx, e := range entries {
		if !used[idx] {
			ordered = append(ordered, e)
		}
	}

	if len(ordered) == 0 {
		return tag, nil
	}
	var b strings.Builder
	b.WriteString(tag)
	b.WriteString(" {")
	for idx, e := range ordered {
		if idx > 0 {
			b.WriteString(", ")
		}
		b.WriteString(formatEntry(e))
	}
	b.WriteString("}")
	return b.String(), nil
}

func formatEntry(e Entry) string {
	if e.Flag {
		return e.Key
	}
	if e.Quoted || needsQuote(e.Value) {
		return e.Key + "=" + quote(e.Value)
	}
	return e.Key + "=" + e.Value
}

// needsQuote reports whether a value cannot be written bare (§9).
func needsQuote(v string) bool {
	if v == "" {
		return true
	}
	for i := 0; i < len(v); i++ {
		if isBareTerm(v[i]) {
			return true
		}
	}
	return false
}

func quote(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' || v[i] == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	b.WriteByte('"')
	return b.String()
}

func validateKey(key string) error {
	if key == "" {
		return &SyntaxError{Msg: "empty key"}
	}
	if key[0] < 'a' || key[0] > 'z' {
		return &SyntaxError{Msg: fmt.Sprintf("key %q must begin with a lower-case letter", key)}
	}
	for i := 1; i < len(key); i++ {
		if !isKeyChar(key[i]) {
			return &SyntaxError{Msg: fmt.Sprintf("key %q contains an invalid character", key)}
		}
	}
	return nil
}

func validateTag(tag string) error {
	for i := 0; i < len(tag); i++ {
		if isSpace(tag[i]) || tag[i] == '{' || tag[i] == '}' {
			return &SyntaxError{Offset: i, Msg: fmt.Sprintf("tag %q contains an invalid character", tag)}
		}
	}
	return nil
}
