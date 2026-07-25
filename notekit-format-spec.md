# notekit Format Specification — v1

> The normative definition of a notekit notebook file. Tool-independent: any
> conforming tool (clinote v2, sqlnote, future notebooks) reads and writes this
> format. Derived from the adjudicated requirements harvest (`notekit-harvest.md`,
> 16 July 2026). Anything not specified here is out of scope for the format.

Status: draft v1.

Revised since first draft:

- **Cell identity (§5, §8)** — stored opaque `id` for identity, derived slug for
  naming. Supersedes harvest D1's slug-as-identity, which silently reassigned
  artifacts when cells were reordered.
- **Sections and result position (§4)** — `section` is now the single delimiting
  concept, running to the next heading of *any* level; the old level-based `span` is
  gone. Result position is defined once and admits exactly three result forms.

One `VERIFY AGAINST PRIORTOOL` placeholder remains (sidecar directory name and payload
shape, §8) — settle from priortool source before freezing.

---

## 1. Overview

A notekit notebook is a single plain CommonMark file with YAML front matter. One
file is one notebook; the file is the artifact and any app is merely a runner over
it. The file must:

- render faithfully on GitHub with no app-specific cruft visible in rendered output;
- remain grep-able and diff-friendly;
- round-trip: parsing then re-serialising an unedited notebook is **byte-identical**.

The format defines *syntax and structure only*. With the few reserved exceptions
below, it assigns no semantics to metadata keys — interpretation belongs to the
runtime layer of each tool (harvest D6/D7).

## 2. Front matter

A notebook begins with a YAML front matter block delimited by `---` lines.

Reserved keys (the complete set):

| Key | Required | Meaning |
|---|---|---|
| `notekit` | yes | Format version. Integer. This spec defines `1`. |
| `title` | no | Notebook title. Tools may display it; absence is not an error. |

All other keys are **passthrough**: preserved byte-for-byte on round-trip, exposed
to the runtime uninterpreted. Tool-specific keys should be namespaced by convention
(`clinote-session`, `sqlnote-db`) but the format does not enforce this.

A file without front matter, or without `notekit: 1`, is not a notekit notebook.
Conforming tools must refuse it rather than guess.

## 3. Document structure

Everything after the front matter is CommonMark. Two constructs have meaning to the
format; all other content is **prose** and is never touched by any tool:

1. **Cells** (§4) — heading + source fence.
2. **Results** — tool-written, occupying a cell's *result position* (§4.2) in one of
   exactly three forms: an `output` fence (§6), an `error` fence (§7), or a sidecar
   reference (§8).

The format is *conservative*: a construct that fails to match the rules below is
prose, not an error. Only tools produce format errors, and only about blocks they
are asked to run or write.

## 4. Cells

A **cell** is:

- an ATX heading, level 2–6 (`##` recommended and used in all examples), followed by
- optional prose paragraphs, followed by
- exactly one **source fence**: a fenced code block whose info string begins with a
  language tag (§9), and which is the first fenced block of *any* kind in the
  heading's section (§4.1).

### 4.1 Sections

A heading's **section** runs from that heading to the next ATX heading of **any**
level, or end of file.

Heading level plays no part. A `##` heading followed by a `###` heading owns only the
content between the two; each is a candidate cell in its own right, and **no cell ever
contains another**.

This flatness is deliberate. A region delimited by heading *level* — running to the
next heading of the same or higher level — would let one run of bytes be simultaneously
an inner cell's source fence and prose belonging to an outer cell, with no rule to
decide which claim wins. Nothing is lost by the flat definition: prose is preserved
verbatim wherever it falls, and result pairing depends only on adjacency to the source
fence (§4.2), never on section size.

### 4.2 Result position

A cell's **result position** is the region immediately following its source fence,
extending across consecutive result constructs with blank lines permitted between
them. A result construct is one of exactly three forms:

| Form | Defined in |
|---|---|
| `output` fence | §6 |
| `error` fence | §7 |
| sidecar reference — provenance comment plus image link | §8 |

Everything in result position belongs to the cell as its result.

- **On read**, more than one construct is tolerated, including a mix of forms. A
  notebook may carry several from an earlier tool version or a hand edit; all of them
  are that cell's results. Tolerance here is not laxity — because a run replaces the
  whole region (below), the condition self-heals rather than needing an error.
- **On write**, a run produces exactly **one** construct, and it replaces the entire
  result position (§6: results are volatile). Whichever of the three forms the result
  kind dictates, a conforming tool never leaves two.

Result position ends at the first thing that is neither a result construct nor a blank
line. From there to the end of the section is prose.

### 4.3 What is not a cell

The format is conservative here; none of the following is an error (§3):

- A section with no fenced block, or none whose info string begins with a language
  tag, is ordinary prose structure.
- A section whose first fenced block is untagged, or tagged `output`/`error`, contains
  no cell. "First fenced block of *any* kind" is meant strictly, so that no tool ever
  has to guess which fence is the source.
- A language-tagged fence that is not first in its section is an inert example.

This is the whole cell-detection rule; there is no runnable-marker syntax.

## 5. Cell identity

Identity and naming are **two separate things**, deliberately not unified:

| | Purpose | Stored? | Survives heading edit? |
|---|---|---|---|
| `id` (§5.1) | durable identity | yes, in the source fence | yes |
| slug (§5.2) | human-readable name | no, recomputed on parse | no, by design |

The two requirements pull in opposite directions — an identity must survive heading
edits, a readable name must track them — so one token cannot serve both. Any scheme
that makes a heading-derived name carry identity either loses artifacts on rename or
reassigns them on reorder.

### 5.1 `id` — durable identity

An opaque token stored in the source fence's info string under the reserved key `id`
(§9):

- **Stored, never derived.** No part of the heading, body, or document position
  contributes to it, so it cannot be invalidated by editing any of them.
- **Immutable.** Once written, a tool never rewrites or reassigns a cell's `id`.
- **Format:** exactly 8 characters from `[a-z2-7]` (lowercase base32), randomly
  generated. Implementations must allow the generator to be injected, so the
  conformance corpus (§11) can be deterministic.
- **Lazily assigned.** A tool writes an `id` only when the cell first needs durable
  identity — that is, when it produces a result whose durable form is a sidecar
  artifact (§8). Cells with inline `output`/`error` results never acquire one: those
  are paired positionally in result position (§4.2) and need no identity. Most cells
  in most notebooks therefore carry no `id`, and their source fences stay clean.
- Assignment is a byte-range splice of the info string, appending the key (§9, §10).
- **Duplicate `id` within one notebook is a tool error.** It can only arise from
  copy-pasting a cell that already carries one; the remedy is to delete the token
  from the copy and let the tool assign a fresh one on next run.

A cell without an `id` has no durable identity. That is the normal state, not a
deficiency.

### 5.2 slug — derived name

Recomputed on every parse from the heading text. Its only jobs are to make sidecar
filenames legible (§8) and to name cells in tool diagnostics. It carries **no**
identity, so nothing breaks when it changes.

- lowercase the heading text;
- map every maximal run of characters outside `[a-z0-9]` to a single `-`;
- trim leading and trailing `-`;
- truncate to 60 characters, then trim any trailing `-` again — this bounds the
  resulting filename well inside filesystem limits;
- an empty result is permitted and is **not** an error (§8 defines the filename in
  that case). A heading of `## 日本語` or `## ⚙️` is valid CommonMark; the format does
  not reject a document over a naming concern.

Two cells may share a slug. This is **not** a collision and requires no
disambiguation — sidecar filenames are made unique by their `id` component (§8).

There are no positional suffixes anywhere in this format. A disambiguator derived from
document order would silently reassign identity when cells are reordered or when a
cell with a duplicate heading is inserted, attaching artifacts to the wrong cells with
no error raised. Deriving identity from content rather than position is a requirement
(harvest R7) that survives the move to slugs.

> **OPTIONAL ALIGNMENT WITH PRIORTOOL** — priortool has its own slug normalisation. Under
> this scheme slug rules are cosmetic, so a divergence costs only filename aesthetics
> and is no longer a correctness concern; aligning is nice-to-have, not a gate.
> (This replaces harvest open question 4, which assumed slugs carried identity.)

## 6. Result blocks: `output`

A tool that runs a cell writes the result as a fenced block in the cell's result
position (§4.2), replacing everything already there — results are **volatile**
(harvest D4):

````markdown
## Disk usage by top-level directory

```sh {format=csv}
du -d1 -h | sort -hr | head -5
```

```output {format=csv, run="2026-07-16T09:41:07Z", tool="clinote/2.0"}
size,path
1.2G,./data
480M,./vendor
```
````

Rules:

- Info string: tag `output`, then metadata (§9). Reserved keys on `output`:
  - `format` — the result kind: `text` (default when absent), `csv`, `jsonl`.
    Extension kinds per the rendering contract.
  - `run` — RFC 3339 UTC timestamp of the run that produced this block.
  - `tool` — producing tool and version, `name/version`.
- The body is the captured result, exactly as durably persisted: ANSI escapes
  stripped, trailing newline normalised to exactly one.
- Truncation: if the runtime capped the output, the tool must append a final line
  `[notekit: output truncated at <N> bytes]` and add `truncated` as a flag key.
- **Fence-length safety:** the fence uses N backticks where N = max(3, longest
  backtick run in the body + 1). Applies to `error` blocks equally.

## 7. Result blocks: `error`

A failed run persists as a **first-class error block** — never as degenerate
output (adjudicated 16 July 2026). Same position and pairing rules as `output`: it
occupies the cell's result position (§4.2), and a run writes exactly one construct
there — an `error` fence, an `output` fence, or a sidecar reference, never a
combination.

````markdown
```error {status=127, run="2026-07-16T09:44:12Z", tool="clinote/2.0"}
zsh: command not found: dv
```
````

- Reserved keys: `run`, `tool` as above; `status` — the domain's numeric error
  code where one exists (shell exit status, database error code). Optional.
- Body: the error text as captured — for shell, combined stdout/stderr interleaved
  as produced; for other domains, the engine's error message. Same ANSI-stripping,
  truncation, and fence-length rules as `output`.

## 8. Sidecar artifacts

Non-textual results (graphs, images, any kind whose durable form is not text) live
in sidecar files, not in the notebook (harvest D3/F11).

A cell producing a sidecar result is a cell that needs durable identity, so the tool
assigns it an `id` (§5.1) on the first such run.

- Sidecar directory: `<notebook-stem>.assets/`, beside the notebook file.
- Naming: `<slug>--<id>.<ext>` for the rendered artifact (e.g. `.png`, `.svg`) and
  `<slug>--<id>.json` for the full data payload, persisted so the artifact can be
  re-rendered offline with no live engine. The `id` is parsed back out by splitting on
  the **last** `--`; when the slug is empty the name is `<id>.<ext>` with no separator.
  The slug half is decoration for humans reading diffs and directory listings — every
  lookup keys on the `id`.
- Durable reference in the notebook, written in the cell's result position (§4.2):

````markdown
## Module wiring for CIP_SUPPLY

```cypher {id=a7f3k2p9}
MATCH (m:Module)-[r]->(n) RETURN m, r, n
```

<!-- notekit:result kind=graph run="2026-07-16T10:02:55Z" tool="graphtool/2.0" -->
![Module wiring for CIP_SUPPLY](procsim-audit.assets/module-wiring-for-cip-supply--a7f3k2p9.png)
````

The HTML comment is the provenance marker (invisible on GitHub); the image link is
standard CommonMark and renders everywhere. The comment's attribute syntax is the
§9 metadata grammar without braces.

**The comment and the link together are one result construct** (§4.2), and the comment
is what makes the link a result rather than prose:

- a provenance comment with no image link following it is prose;
- an image link with no provenance comment before it is prose — **always**. Users put
  images in notebooks; the format must never mistake one for a result it may overwrite.

### 8.1 Lifecycle under editing

**Heading rename.** The slug changes, the `id` does not. The tool therefore knows the
artifact still belongs to this cell: it renames both sidecar files to the new slug and
rewrites the image link. No orphan is created and no re-run is needed. Fixing a typo in
a heading costs nothing.

**Reorder and duplicate headings.** Neither affects any `id`, so neither affects any
attachment. Nothing to detect, nothing to repair.

**Orphans.** A sidecar file whose `id` component matches no `id` in the notebook is an
orphan — which now means precisely one thing: the cell was deleted (or its `id` was
stripped by hand). Tools must report orphans and must never delete them silently.

> **VERIFY AGAINST PRIORTOOL** — the directory name and the payload JSON shape are
> written from the priortool spec; check them against the priortool implementation and
> adjust whichever diverged before freezing. The **naming convention** above is not in
> scope for that check — `<slug>--<id>` is normative here and predates no priortool
> equivalent, since priortool had no stored id.

## 9. Info-string metadata grammar

The info string of any format-significant fence is:

```
info-string  = tag [ SP metadata ]
tag          = language tag | "output" | "error"
metadata     = "{" entry ( "," entry )* "}"
entry        = key [ "=" value ]
key          = [a-z] [a-z0-9_-]*
value        = bare | quoted
bare         = one or more chars excluding space , = { } "
quoted       = '"' ( any char, \" and \\ escaped ) '"'
```

- Whitespace around entries, commas, and `=` is insignificant.
- A key without `=value` is a boolean flag, true.
- Duplicate keys in one info string are a **tool error** (fail loud).
- Entry order is not significant. Unknown keys are passthrough: preserved on
  round-trip, delivered to the runtime uninterpreted.
- When a tool *writes* a block it emits canonical form: single spaces after commas,
  reserved keys first in the order defined in §6/§7, then passthrough keys in
  original order. Blocks the tool did not write are never reformatted.
- **`id` insertion is append-only.** Source fences are usually hand-authored, so
  adding an `id` (§5.1) must not reformat them: the entry is appended after all
  existing entries, which keep their original text, spacing, and order. A fence with
  no metadata gains ` {id=…}` directly after its tag; a fence with metadata gains
  `, id=…` immediately after the final entry — *before* any whitespace preceding the
  closing brace, so `{format=csv }` becomes `{format=csv, id=… }`. The edit is
  strictly additive: no existing byte is removed or moved. This is a deliberate
  exception to the reserved-keys-first rule above, which governs tool-written result
  blocks only.
- Escaping inside quoted values is a bijection: `\` may precede only `"` or `\`, and
  any other escape is malformed. Without that, decode and re-encode could not agree
  byte for byte.

The format reserves the keys named in §5–§8: `id` on source fences, and the result and
provenance keys on `output`/`error`/sidecar markers. Every *other* key on a **source**
fence (e.g. `{format=csv}` as a rendering hint, session or connection selectors) is
cell-level metadata for the runtime, uninterpreted by the format (harvest D6).

## 10. Round-trip policy

- Parse → serialise of an unedited notebook is byte-identical. Normative.
- The implementation posture that achieves this (harvest P2) is byte-range splice:
  a Markdown parser is used as a structural scanner only; tools rewrite exactly the
  byte ranges of blocks they executed or edited and copy every other byte through.
- The complete set of writes a tool may perform is: (a) result blocks in result
  position (§6, §7), (b) sidecar references and their provenance comments (§8),
  (c) appending an `id` to a source fence's info string (§5.1, §9), and (d) prose
  ranges the user explicitly edited. Note that (c) means **running a cell can modify
  its source fence**, not only its results — narrowly, append-only, and at most once
  per cell in its lifetime.
- Tools must never reflow prose, normalise whitespace, reorder metadata they did
  not write, or "fix" non-conforming constructs.

## 11. Conformance corpus

The format ships with a golden-file corpus; a conforming implementation passes all:

1. Round-trip identity over every corpus file (parse → serialise, byte-compare).
2. Cell detection: files mixing cells, inert example fences, and fences with no
   heading. Section boundaries (§4.1): a `##` cell immediately followed by a `###`
   cell, each detected independently with neither containing the other; a `###`
   heading interposed between a source fence and an `output` fence, which breaks the
   pairing. Non-cells (§4.3): first fence untagged, and first fence tagged `output`.
3. Result position (§4.2): a single construct of each of the three forms; two `output`
   fences and a mixed `output`-plus-sidecar-reference both read as one cell's results
   and both replaced wholesale by one construct on run; a blank line inside result
   position, and a paragraph terminating it. Sidecar-reference pairing (§8): a bare
   image link with no provenance comment is prose and survives a run untouched.
4. Metadata grammar: valid/invalid info strings, quoting, flags, duplicate-key
   rejection.
5. Result splice: run a cell, verify only the expected byte range changed.
6. Fence-length safety: bodies containing 3, 4, 5-backtick runs.
7. Slug derivation: normalisation, 60-character truncation, empty result from a
   non-ASCII heading, and two cells sharing a slug with no suffix applied to either.
8. `id` assignment: appended to a fence with no metadata and to one with existing
   metadata, in both cases leaving every other entry's text, spacing, and order
   byte-identical; not assigned at all to a cell whose result is inline; duplicate
   `id` in one notebook rejected as a tool error.
9. **Identity stability** — the cases the pre-`id` scheme failed:
   - heading rename keeps the attachment: sidecar files renamed, `id` unchanged, image
     link rewritten, no orphan reported, no re-run required;
   - reordering two cells, and inserting a third cell with a duplicate heading above
     them, leaves every sidecar attachment unchanged;
   - a sidecar whose `id` matches no cell is reported as an orphan and left on disk.
10. Error block form, truncation marker, ANSI stripping.

## 12. Non-goals (format v1)

- No compatibility with clinote v1 files (harvest D8).
- No cell chaining syntax, no cross-cell references.
- No crystallised-result or staleness syntax in core — extension territory
  (rendering contract) for priortool-class tools.
- No multi-notebook includes, no execution-order syntax beyond document order.
- No rich-text constructs; prose is plain CommonMark.

## FUTURE (recorded, do not implement)

- Named-relation metadata (`as=`) for the polyglot/SQLite-join notebook.
- Streaming-friendly incremental result form.
- Crystallised lifecycle keys (`crystallised`, staleness stamps) standardised once
  a second crystallising tool exists.
