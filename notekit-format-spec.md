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
- **Sidecar lifecycle (§8.1)** — a run removes *superseded* artifacts of the cell it
  ran, while orphans of deleted cells remain report-only. Closes the gap where a cell
  that stopped producing a sidecar left files behind that no rule reached.
- **Permitted writes (§10)** — the list is now grouped into writes a *run* makes and
  writes a *user* asks for, and gained the two that were missing: editing a source
  fence's body, and inserting or removing a whole section.
- **Sidecar payload and atomic writes (§8)** — corrected against the priortool
  implementation; see the note at the end of §8.
- **Moving a section (§10 h, §10.1)** — reordering cells is admitted as a permitted
  write, with the section's bytes moved verbatim. §10.1 states what happens to the
  whitespace at a seam, for insertion and removal as well as movement, and says plainly
  what is *not* guaranteed.

**No placeholders remain.** §8 was verified against the priortool implementation: the
directory name is confirmed, the payload rule was too thin and is corrected, and atomic
artifact writes are adopted. The `<slug>--<id>` naming is a deliberate divergence, and
priortool's own manual reattach screen is the evidence for it.

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
| `notekit-tool` | no | Advisory: the tool the notebook was written for (§2.1). Reserved so tools agree on its spelling; it never decides what runs. |

All other keys are **passthrough**: preserved byte-for-byte on round-trip, exposed
to the runtime uninterpreted. Tool-specific keys should be namespaced by convention
(`clinote-session`, `sqlnote-db`) but the format does not enforce this.

A file without front matter, or without `notekit: 1`, is not a notekit notebook.
Conforming tools must refuse it rather than guess.

### 2.1 The engine is derived; `notekit-tool` is advisory

Two rules that must not be confused:

- **What a notebook can run is derived from the info-string tags its cells carry**
  (§9), decided per cell by comparing each tag with the executor's. A file of `sql`
  cells is a SQL notebook because its cells say `sql`. This is normative, and nothing
  in front matter takes part in it.
- **`notekit-tool` names the application a notebook was written for, as a hint.** It is
  reserved so that tools agree on its spelling, and it is **advisory**: it never
  decides what runs.

#### Why the engine is derived rather than declared

A declaration that *decided* the engine was considered and rejected. The reasoning is
recorded because the idea recurs naturally:

1. **It would be redundant, and redundancy needs a conflict rule.** The engine is
   already stated once per cell. A key competing with those tags would force the format
   to define which wins — structurally the mistake D1 made, two jobs conflated into one
   field, failing quietly. The identity split in §5 exists because that failure mode
   already cost this format one redesign.
2. **It would couple the file's meaning to a binary.** The file is the artifact and any
   application is merely a runner over it. What a notebook *is* should not depend on
   what a tool was called when it was written.
3. **A hand-edited key is expected to go stale.** Notebooks get copied and tools get
   renamed. A key that decided the engine would then misidentify the file to every tool
   that read it, including tools that could otherwise have worked it out correctly.

#### Why the advisory key exists anyway

Derivation answers *what can run this*. It cannot answer *what should I run instead*,
and that gap is real: notebook tools live in their own repositories, so any list of
tool names compiled into one tool covers only those shipped beside it. A notebook
naming its own tool can point at an application the reading tool has never heard of.

Hence the split — the key carries the suggestion, the cells carry the meaning.

#### Normative rules for `notekit-tool`

- A tool **must not** refuse a notebook because the key names a different application,
  and **must not** prefer the key over the cells. It takes no part in deciding what
  runs.
- A tool **should** use it to name the application to try when it cannot run a notebook
  itself, in preference to any list of its own — only the file can name a tool the
  reader does not know.
- A tool **should** report a key that contradicts the cells as a *warning*, and carry
  on. Contradiction is detectable only when the named tool's language is known to the
  reader; otherwise silence is correct.
- A tool **may** write it when creating a notebook (§10 g), which is the one moment the
  correct value is known for certain.
- It is a scalar naming one application. Absence is not an error and never will be:
  every notebook written before this key existed lacks it.

#### The starter-cell consequence

A notebook with *no* cells has no tag to derive an engine from — the one case
derivation cannot answer. This is why a tool's notebook-creating command writes a
starter cell of its own language rather than an empty file (§10 f, §10 g). The starter
cell, not the key, is what keeps every notebook's engine knowable: the key is advisory
and may be absent or wrong, so it cannot carry that weight.

A tool **should** refuse a notebook none of whose cells it can run, and say so before
it starts work rather than once per cell at run time. A notebook with no cells, or one
where only *some* cells match, must still be accepted: execution is checked per cell,
so refusing the whole file would be stricter than the format.

**When the derivation rule itself would need revisiting:** if two engines ever share
one language tag — two different SQL backends, say — the tag stops distinguishing them
and derivation becomes genuinely insufficient. `notekit-tool` does not rescue that
case, being advisory by construction. Should a *deciding* declaration become necessary,
declare the **language**, not the application: a language is checkable against the
cells rather than competing with them, and it does not name a binary.

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

**An unclosed source fence has no result position.** A fence left unterminated at end
of file extends to end of file, so there is no position after it. Such a cell is still
a cell — its body is well defined and a tool may run it — but a tool asked to persist
its result must **refuse**. Writing anyway would append into the fence body and
silently corrupt the cell; closing the fence first would be the silent repair §10
forbids. The author closes the fence, and the result becomes persistable.

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

> **Aligned with priortool, and checked.** The rules above match priortool's implementation
> exactly; `doc.TestSlugMatchesPriortool` carries priortool's algorithm as an oracle and
> asserts the two agree, so this is a verified claim rather than an impression. Under this
> scheme slug rules are cosmetic, so a divergence would have cost only filename
> aesthetics — but matching means a notebook migrated from priortool keeps the filenames a
> reader recognises, for free.
>
> One intended difference: priortool requires a non-empty slug, this spec permits an empty
> one, because a non-ASCII heading is valid CommonMark and the format must not reject a
> document over a naming concern. §8 falls back to `<id>.<ext>`, so nothing depends on it.
> (This closes harvest open question 4, which assumed slugs carried identity.)

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
- **Ordering:** truncate, append the marker, *then* compute fence length. Truncation
  can cut inside a backtick run, so a length computed from the untruncated body may be
  too short — or needlessly long — for what is actually written.
- **ANSI stripping scope:** remove CSI sequences (`ESC [` … final byte), OSC sequences
  (`ESC ]` … BEL or ST), and any other `ESC` sequence (intermediates then a final
  byte); then drop every remaining control character except tab and newline. A
  carriage return is therefore dropped, so CRLF becomes LF and a progress-bar CR
  disappears. A shell executor emits far more than colour, and the durable form is the
  plain form (harvest F12).

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
  `<slug>--<id>.json` for its payload. The `id` is parsed back out by splitting on the
  **last** `--`; when the slug is empty the name is `<id>.<ext>` with no separator. The
  slug half is decoration for humans reading diffs and directory listings — every lookup
  keys on the `id`.
- **The `.json` must hold everything an offline re-render needs**, which is more than the
  result data: for a graph it is the data *and* the node coordinates and camera state,
  because a figure reproduced from data alone would lay out differently (harvest V4).
  The exact shape is the kind's business, not the format's — the rendering contract owns
  it — and a tool may add whatever else it needs, in keeping with the format's
  passthrough posture everywhere else. A priortool-class tool records its staleness inputs
  there, which notekit core neither requires nor forbids (harvest D4).
- **Both files are written atomically**: to a temp file in the sidecar directory, then
  renamed into place. A reader must never see a half-written artifact — for a browser
  fetching an image while a run is in flight, that is otherwise exactly what happens.
- Durable reference in the notebook, written in the cell's result position (§4.2):

````markdown
## Module wiring for CIP_SUPPLY

```cypher {id=k3m7q2vf}
MATCH (m:Module)-[r]->(n) RETURN m, r, n
```

<!-- notekit:result kind=graph, run="2026-07-16T10:02:55Z", tool="graphtool/2.0" -->
![Module wiring for CIP_SUPPLY](procsim-audit.assets/module-wiring-for-cip-supply--k3m7q2vf.png)
````

The HTML comment is the provenance marker (invisible on GitHub); the image link is
standard CommonMark and renders everywhere.

The comment's attribute syntax is the §9 metadata grammar with the braces removed and
nothing else changed — **entries stay comma-separated**, and every quoting and
duplicate-key rule carries over unaltered. An earlier draft's example separated them
with spaces alone, which contradicted the grammar it claimed to reuse; commas mean one
grammar, one parser, and one canonical writer for fences and comments alike. The
comment is invisible in rendered output, so nothing is lost by the punctuation. The
marker itself, `notekit:result`, must be the comment's first token — an ordinary HTML
comment is never a result reference.

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

**Superseded artifacts.** A file carrying the `id` of a **live** cell that the latest run
of *that cell* did not write is superseded, and the run removes it. This is the volatile
lifecycle applied to sidecars: a run replaces the whole of a cell's result, so a
previous artifact of the same cell is dead once that run completes — whether the cell now
produces different files, or no sidecar at all.

This is not the silent deletion of user data forbidden above, and the distinguishing
question is whose artifact it is. A superseded file belongs to the very cell the user just
asked to run, and would have been overwritten anyway had its name not changed. An orphan
belongs to a cell that no longer exists, no run touches it, and it is only ever reported.
A tool must not conflate the two.

> **Verified against the priortool implementation** (harvest open question 3), not just its
> spec. Three findings:
>
> - **Directory name confirmed.** priortool derives it exactly as above.
> - **The payload rule above was too thin** and has been corrected. priortool persists one
>   JSON per artifact set holding provenance, camera state, per-node positions, and the
>   result graph verbatim. "The full data payload" — this spec's previous wording — would
>   not reproduce the figure. The rendering contract already said "data and node
>   coordinates", so it was §8 alone that had drifted from its sibling.
> - **Atomic writes adopted** from priortool, which does this for the same reason.
>
> The **naming convention** is a deliberate divergence, and priortool is the evidence for
> it. priortool names artifacts `<cell-slug>.png` / `<cell-slug>.json`, so a heading rename
> orphans them — and it therefore ships a manual *reattach* screen described in its own
> source as "the fix for a renamed heading, whose old artifacts no longer match any cell
> ID". §5's stored `id` is what removes the need for that screen: here a rename renames
> the files (§8.1). The hazard §5 was written against is not hypothetical; it is a
> shipped feature working around it.

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
- The complete set of writes a tool may perform falls into two groups, and the
  distinction is what makes the list closed rather than arbitrary.

  **Writes a run makes**, without being asked beyond "run this cell":

  - (a) result constructs in result position (§4.2, §6, §7);
  - (b) sidecar artifacts and their references, including removing the run cell's own
    superseded artifacts (§8, §8.1);
  - (c) appending an `id` to a source fence's info string (§5.1, §9). This means
    **running a cell can modify its source fence**, not only its results — narrowly,
    append-only, and at most once per cell in its lifetime.

  **Writes the user asks for explicitly**, each addressing one construct:

  - (d) a prose range the user edited;
  - (e) a source fence whose body the user edited. The whole fence is re-emitted, not
    just the body, because fence-length safety (§6) applies to a source fence too: a
    body containing a fence-length run of the fence character would otherwise
    terminate its own fence and turn the rest of the cell into prose. Widening is
    permitted here precisely because the fence is what is being edited;
  - (f) inserting a new section, or removing an existing one, at a section boundary.
    A new cell is a heading plus a source fence (§4) and nothing else — a tool must
    not invent prose, metadata, or a result to go with it.
  - (g) creating a whole notebook that did not exist: front matter carrying
    `notekit: 1`, an optional `title` and optionally `notekit-tool` (§2.1), then one
    cell per (f). Nothing else — in particular no tool-specific configuration key,
    which the tool would only be guessing at. The one cell is required rather than
    optional: an empty notebook has no info-string tag, and §2.1 derives the engine from
    exactly those tags. **An existing file is never a target of this write** — the file is the
    artifact, so a tool asked to create over one must refuse rather than overwrite.

  - (h) **moving a whole section to another section boundary** — reordering cells.
    The unit is a section (§4.1), heading through to the next heading of any level, so
    a cell's prose and its result travel with it; there is no way to move a source fence
    out of its own section.

    The section's bytes are moved **verbatim**. A move is not a removal followed by an
    authoring of similar content: the source fence must not be re-rendered, its info
    string must not be reordered or requoted, and its `id` must not be reassigned. A
    tool that implements a move by round-tripping the cell through its cell *writer*
    will violate this, because the writer's job is to normalise.

    Identity is unaffected, and not by accident: `id` is stored and derived from
    nothing (§5.1), so a move is inert for identity and for every sidecar attachment.
    §11.9 already requires exactly this of a reorder. Under the superseded D1 scheme,
    where a positional suffix disambiguated duplicate headings, this write would have
    silently reattached artifacts to the wrong cells — which is why it is admissible
    now and would not have been then.

    The topmost destination is the start of the **first section**, not the start of the
    body: a notebook's preamble (§3) is prose belonging to no cell, and a moved cell
    must land after it rather than above it.

    A move with nothing to do — the first cell moved up, the last moved down, or a
    notebook of one cell — **must not write the file at all.** The file is the artifact,
    and a no-op that still rewrites it produces a spurious change for a reader,
    a diff, and any watcher.

    **A section that is not self-contained must not be moved, and the move is refused.**
    An unclosed fence runs to end of file (§4.2), so it is harmless only while its cell
    is last: relocate it and every following heading and fence becomes its body, which
    silently collapses cells into one. Moving another cell *past* such a section does the
    same thing, because a swap relocates both — so either participant being unclosed is a
    refusal. Closing the fence first would be the silent repair this section forbids.

    Because a section's self-containment is not decidable from the source fence alone, a
    tool **should** confirm its own move rather than reason about which cases exist:
    apply the edits, re-read, and check the notebook still holds the same cells before
    committing the write. A fuzzer found a second shape of this hazard within minutes of
    the operation existing, which is the evidence for preferring a check to an argument.

  Group (d)–(h) is the reason this list is not simply "results": a notebook a user
  cannot edit is a report, not a notebook. What unites the whole list is that every
  entry names a construct the format defines, so a tool never has to guess which bytes
  are safe to touch.
- Tools must never reflow prose, normalise whitespace, reorder metadata they did
  not write, or "fix" non-conforming constructs.

### 10.1 Whitespace at a seam

Writes (f), (g) and (h) all cut or paste at a **section boundary**, and a boundary is
the one place where whitespace is ambiguous about which cell it belongs to. A blank line
separating two sections falls *inside the earlier section's span*, because a section
runs to the next heading (§4.1) and the blank line comes before it.

Two rules follow, and they are deliberately asymmetric:

- **Inserting** a section normalises the seam: the heading is placed at the start of a
  line, with one blank line above it unless one is already there, and separated from
  whatever follows. A heading that does not begin a line is not a heading, so this much
  is forced rather than cosmetic.
- **Removing** a section takes its span exactly, and therefore carries away the
  separator that sat at its end.

**What is therefore not guaranteed:** inserting a section and then removing it returns a
document with the same *cells* but not necessarily the same *bytes*. The seam whitespace
may differ by one blank line.

For a **move**, more is achievable and worth aiming at, though it cannot be promised in
general. Treating the trailing blank lines as belonging to the *position* rather than to
the section — exchanging two sections' contents and leaving each slot's separator where
it sits — makes a move exactly symmetric, so moving a cell away and back restores the
file byte for byte. That holds whenever both sections are self-contained, which is the
normal case and is worth a test. It cannot be guaranteed for every input, because a
section containing an unclosed fence changes what the bytes after it mean; such a move
is refused outright (§10 h), so the guarantee is *reversible or refused* rather than
reversible always.

One trap in implementing that: content taken from the end of the file carries no trailing
newline, so appending the destination slot's separator to it yields a single line ending
rather than a blank line, and the seam quietly loses its blank line. Give the content its
own line terminator first.

This does not weaken §10's round-trip guarantee, which is about parsing and
re-serialising an **unedited** notebook — a file nobody asked to change is never
rewritten. It is a statement about edits composing, and it is worth writing down
because the asymmetry is invisible until two edits are combined.

*Considered and not adopted:* normalising the seam on removal as well, which would make
move-there-and-back byte-identical in the common case. It was rejected for now because
it changes the behaviour of (f) — a write that already ships and whose golden files
encode the current shape — for a property no tool has asked for. A tool that wants a
reversible move can achieve it without a format change, by moving the section back to
the boundary it came from rather than to a recomputed one.

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
   position, and a paragraph terminating it; an unclosed source fence, whose cell is
   readable but whose result a tool must refuse to write. Sidecar-reference pairing
   (§8): a bare image link with no provenance comment is prose and survives a run
   untouched.
4. Metadata grammar: valid/invalid info strings, quoting, flags, duplicate-key
   rejection.
5. Result splice: run a cell, verify only the expected byte range changed.
6. Fence-length safety: bodies containing 3, 4, 5-backtick runs, including a body
   whose truncation cuts inside a backtick run — the fence must clear the run that
   survives, not the original.
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
10. Error block form, truncation marker, and ANSI stripping across CSI, OSC, other
    escape sequences, and stray control characters.
11. Sidecar naming: `<slug>--<id>` written and split on the last `--`, an empty slug
    yielding `<id>` alone, and a slug that itself contains `--`.
12. Sidecar lifecycle: a run removing a superseded artifact of the cell it ran while
    leaving an orphan of a deleted cell untouched.
13. Sidecar writes are atomic: a temp file in the sidecar directory renamed into place,
    leaving nothing behind on failure.
14. Document edits (§10 d–f): a prose range replaced; a source body replaced, including
    one that forces the fence wider; a section inserted at each boundary — before the
    first cell, between two cells, after the last, and into a notebook with none — and a
    section removed from each of those positions. Every case round-trips and leaves every
    other cell byte-identical.
15. **Moving a section (§10 h)**: a cell moved up and down across every boundary,
    including past a cell whose section carries prose and a result. Each case must leave
    the moved section's bytes unchanged, every other cell byte-identical, every `id`
    unchanged, and every sidecar attachment intact. Also required: the first cell moved
    up, the last moved down, and a one-cell notebook each write **nothing**; and moving
    the first cell downwards leaves the preamble in place rather than carrying it along
    or landing above it. A move involving a section that is not self-contained — an
    unclosed fence anywhere inside it — is **refused**, in both directions, yielding no
    edits. Moving a cell away and back is byte-identical wherever the move is permitted,
    including across seams with irregular blank lines and a file with no trailing
    newline.

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
