// Package doc implements the notekit document model: parsing a notebook into
// byte-ranged constructs and splicing exact byte ranges back out.
//
// Implements notekit-format-spec.md §2–§8 — front matter, cell detection, cell
// identity (stored id plus derived slug, §5), result-block pairing, and sidecar
// references and their bookkeeping.
//
// goldmark is used as a structural scanner only (harvest P2); every construct
// records its exact byte span and this package owns all serialisation. There is
// deliberately no API that serialises a whole document tree — splice is the only
// write path, and that absence is what makes §10's byte-identical round-trip
// guarantee achievable.
//
// Front matter is handled as an opaque byte range beyond the two reserved scalars
// (notekit, title), which is how §2's byte-for-byte passthrough is guaranteed
// without round-tripping YAML through a marshaller.
//
// This package owns id assignment and its invariants: lazy (only for cells whose
// result is a sidecar artifact), append-only, immutable thereafter, duplicates
// rejected as a tool error. The generator is injectable so goldens stay
// deterministic.
//
// The format conformance corpus (§11) lives with this package, under testdata,
// and is its acceptance suite.
package doc
