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
// This package assigns no meaning to any key. Callers interpret.
package meta
