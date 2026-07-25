# notekit — Requirements Harvest

> Evidence base for the notekit specification. This document inventories the mechanics
> of the three existing notebook tools, classifies each requirement as **proven**,
> **divergent**, or **speculative**, and records the adjudications the spec must make.
> Nothing here is design; it is the record of what has already been designed three times.

Status: adjudicated 16 July 2026 — divergences resolved in §3 · Provenance markers: `[cli]` clinote, `[cy]` graphtool, `[cyp]` priortool
Items marked ⚠ are inferred from incomplete records — confirm or correct before the spec inherits them.

---

## 1. Source corpus

| Tool | Domain | Executor | UI surface | Status |
|---|---|---|---|---|
| **clinote** | Shell | Persistent shell under a pty, sentinel exit-code capture | Go/Echo + HTMX, browser | Most advanced; implemented |
| **graphtool** | Cypher | Embedded GraphStore (in-memory property graph, SQLite-backed, file-per-graph) | SwiftUI editor + WKWebView preview (cmark-gfm, Sigma.js) | Spec'd; implementation status ⚠ |
| **priortool** | Cypher figure plates | Neo4j via priortool-style connection | Go/Echo, browser Sigma.js, crystallise-to-PNG, PDF export | Spec'd (six milestones) |

All three independently converged on: one CommonMark file per notebook, fenced blocks as
cells, results persisted into or beside the file, single static Go-or-native binary,
offline capability, plain-text durable artifacts.

---

## 2. Proven requirements

Agreed by two or more tools, or validated in one with no counter-evidence.
These go into the notekit spec substantially as-is.

### 2.1 Format layer

| # | Requirement | Provenance |
|---|---|---|
| F1 | One `.md` file is one notebook. The file is the artifact; the app is a runner. | cli, cy, cyp |
| F2 | The file is plain CommonMark. Renders faithfully on GitHub; grep-able; diff-friendly; no app-specific cruft visible in rendered output. | cli, cy |
| F3 | YAML front matter carries notebook-level metadata. | cli, cyp |
| F4 | A cell is a fenced code block; the info string declares the language/executor tag. | cli, cy, cyp |
| F5 | Info string carries per-cell options as structured metadata beyond the language tag. | cli |
| F6 | Output is persisted as a fenced block adjacent to its source cell, paired positionally. | cli, cy ⚠ |
| F7 | Output blocks are tagged (`output`-style info string) so they are distinguishable from source cells and from ordinary fenced prose examples. | cli |
| F8 | Fence-length safety: fences grow when body content contains backtick runs. | cli |
| F9 | Round-trip policy: parse → serialise of an unedited notebook is byte-identical. Implemented as byte-range splice — the tool never regenerates prose it did not touch. | cli |
| F10 | Provenance marker on machine-written output (`graphtool:run`-style stamp). | cy |
| F11 | Non-textual results live as sidecar files beside the notebook (PNG + JSON per cell), referenced from the markdown by a standard image link. Full data payload persisted in the sidecar so re-render needs no live database. | cyp |
| F12 | ANSI is stripped from on-disk output; colour is rendered live in the browser only. Generalises to: the durable form is the plain form. | cli |

### 2.2 Runtime layer

| # | Requirement | Provenance |
|---|---|---|
| R1 | One persistent session per notebook, owned by the executor, carrying state between cells (cwd/env/functions for shell; graph binding for Cypher). Lifetime = server process. | cli, cy |
| R2 | Self-contained vs bound data modes: a notebook either defines its data (cells build it; run-all from empty reconstructs) or references an external store. This is a general property, not a Cypher quirk. | cy |
| R3 | Async execution: run request returns immediately; UI polls (500 ms) with spinner; output populates on completion. No streaming. | cli |
| R4 | Output cap (1 MiB) with explicit truncation behaviour. | cli |
| R5 | Auto-save on successful run; visible unsaved-changes indicator for prose edits. | cli |
| R6 | Staleness detection: a persisted result knows whether its source cell has changed since it was produced. Downstream consumers (PDF export) refuse on stale by default, with an explicit `--force` that visibly marks forced output. | cyp |
| R7 | Cell identity derived from content (heading SHA-256 hash), not from position, where durable identity is needed (sidecar naming, staleness). | cyp |
| R8 | Exit/error status captured per cell (sentinel-based for shell). | cli |
| R9 | Offline-first: all assets (renderer JS, CSS, fonts) vendored and embedded (`go:embed` / app bundle); no CDN or network round-trip to render. | cy, cyp |

### 2.3 Rendering layer

| # | Requirement | Provenance |
|---|---|---|
| V1 | Core result kinds: `text` (default), `csv`, `jsonl` — the latter two render as sortable tables in the live UI (JSONL flattened to columns). | cli |
| V2 | Graph results render live via Sigma.js/graphology (reusing the graphstack stack). | cy, cyp |
| V3 | Print-quality crystallisation happens in the browser (WebGL context lives there); server persists the artifact. Interactive compose → freeze is a legitimate result lifecycle, distinct from pure re-runnable results. | cyp |
| V4 | Deterministic layout/rendering valued over live physics for durable artifacts (seeded layouts, persisted coordinates). | cyp |
| V5 | Prose is editable inline in the UI, but there is no rich editor; source is authoritative. | cli, cy |

### 2.4 Implementation posture (house style, but load-bearing)

| # | Requirement | Provenance |
|---|---|---|
| P1 | Single static binary; Go stdlib-first; no frontend build step. | cli, cyp |
| P2 | Markdown parser (goldmark / cmark-gfm) used as a structural scanner only; the tool's own byte-range model owns serialisation. This is what makes F9 achievable. | cli |
| P3 | Spec written before implementation, with explicit non-goals and a FUTURE list that the implementing agent must not build. | cli, cy, cyp |

---

## 3. Adjudications

Formerly "divergences requiring adjudication"; resolved in review, 16 July 2026.

**D1 — Cell identity: slug.** ✅ Identity is a human-readable slug derived from the
cell heading, per the approach explored in later priortool versions — superseding the
heading-SHA-256 scheme in the original priortool spec. The slug names sidecars and keys
staleness. ⚠ Exact slug rules (normalisation, allowed characters, collision handling
for duplicate headings) to be lifted verbatim from the current priortool implementation
rather than reinvented — see open question 4. Positional adjacency remains the
output-pairing rule within a cell's span.

**D2 — UI surface: Go/Echo/HTMX only.** ✅ notekit v1 targets the Go/Echo/HTMX
surface exclusively. A SwiftUI notebook would be a separate implementation borrowing
ideas where relevant, not a consumer of this kit. The kit still keeps document model,
runtime, and server as distinct packages — good layering for its own sake, not UI
abstraction as a requirement.

**D3 — Result persistence: by kind.** ✅ As leaned: textual result kinds inline as
tagged fenced blocks (F6/F7); non-textual kinds sidecar + standard link (F11). The
kind boundary and the sidecar naming convention (slug-based per D1) are normative
format, not tool behaviour.

**D4 — Result lifecycle: volatile is the rule.** ✅ Results are volatile —
overwritten on every run — and this is the kit's only core lifecycle. Crystallised
results are the exception: a domain capability (priortool-class tools) expressed through
the rendering contract's extension mechanism, not core kit machinery. Consequently
staleness tracking (R6) and browser crystallisation (V3) remain proven requirements
but sit outside kit core v1, alongside crystallisation.

**D5 — Session substrate: per-domain.** ✅ Whether the engine is internal (embedded
store, pty child process) or external (network database) is a runtime-layer decision
made per domain by each executor. The kit contract sees only opaque executor-owned
session state with lifecycle hooks tied to notebook open/close; engine locality never
surfaces above the executor boundary.

**D6 — Info string: generic cell metadata.** ✅ The info string carries generic
cell-level metadata. The format spec owns syntax only — grammar, quoting,
unknown-key passthrough — and defines no key semantics beyond the language tag and
the reserved output/provenance tags. The runtime layer converts entries into whatever
form the executor understands.

**D7 — Front matter: generic notebook metadata.** ✅ Same posture as D6 at notebook
level: the format spec defines syntax and a minimal reserved core; all other keys pass
through uninterpreted for the runtime/executor to consume.

**D8 — Compatibility: none required.** ✅ clinote v2 is not required to open
clinote v1 files. The v1 corpus is evidence for the format spec, not a constraint on
it. Round-trip byte-identity (F9) remains a property of the new format in its own
right — parse → serialise of an unedited notebook is byte-identical — independent of
any v1 relationship.

---

## 4. Speculative — excluded from notekit v1

Ideas discussed but never implemented or validated. Recorded so the spec can name
them as non-goals rather than losing them.

- `$LAST_OUTPUT` cell chaining (clinote, deferred to its v2).
- Streaming output during a run (clinote considered, chose completion-populate). The
  executor contract should not *preclude* streaming, but the kit does not provide it.
- Named relations / SQLite join substrate / multi-headed polyglot notebook (this
  conversation). Depends on a Relation type contract that does not yet exist.
- CI/headless execution of notebooks.
- Multi-user, auth, collaboration, networked notebook server.
- Read-only iPad viewer (graphtool aside).
- Binary output kinds beyond the sidecar image pattern.
- Rich prose editor.

---

## 5. Candidate spec structure

Three documents (or three sections of one), in dependency order:

1. **Notebook format spec** — normative, tool-independent. Cell grammar, info-string
   option grammar (D6), output conventions per result kind, sidecar convention (D3),
   cell identity (D1), front matter core (D7), round-trip policy (F9), provenance
   stamps (F10). The `.gfig`-class document. Ships with a round-trip conformance
   corpus of golden files.
2. **Kit spec** — the Go library. Document model (parse/splice, F9/P2), executor
   contract (language tag, opaque session lifecycle per D5, typed result return), run
   scheduler (R3), save semantics (R5), volatile result lifecycle only (per D4 —
   crystallisation and staleness excluded from core). States responsibilities, not
   signatures, until the format spec is frozen.
3. **Rendering contract** — closed core kind set (`text`, `table`, `error`) plus the
   extension rule: a domain kind is admissible iff it defines (a) its live rendering
   payload and (b) its durable CommonMark form (typed fence or sidecar + link).
   `error` is a **first-class cell format** (adjudicated, 16 July 2026): a failed run
   persists as its own tagged block with a defined on-disk form, not as degenerate
   output.

Validation gates, in order: (1) this harvest reviewed and adjudicated — done,
16 July 2026; (2) format spec drafted, using the v1 corpus as evidence but not as a
constraint (D8), shipping with a round-trip conformance corpus of golden files in the
new format; (3) clinote v2 built on the kit at v1 feature parity, shell executor as a
thin adapter — the parity test is functional, not file-level; (4) only then, a second
consumer (sqlnote or a graphtool successor) to test the abstraction against a non-shell
domain.

---

## 6. Open questions

All reviewed 16 July 2026. Nothing here blocks the format spec draft; items marked
**extract** are lifted from existing source at spec-writing time rather than from memory.

1. ~~clinote's info-string grammar today~~ **Resolved (prior art):** the info string
   carried the cell type (`sh` vs `output`) and the return format (`text`/`csv`/`jsonl`).
   So the proven usage is exactly two axes — *what kind of block this is* and *how to
   interpret/render its body* — which the D6 grammar generalises: language tag first,
   then key=value metadata, with result-kind as one such key.
2. ~~How does clinote persist a failed cell today?~~ **Resolved:** the error report
   is a first-class cell format — its own tagged block, defined in the format spec
   alongside output. Remaining detail for the spec: the block's body shape (message,
   exit status, stderr/stdout separation or interleave).
3. priortool sidecar convention (F11) — as-built state unconfirmed. ⚠ The format spec
   defines the sidecar section normatively from the priortool *spec*; verify against
   the priortool implementation when writing, and adjust whichever diverged.
4. Slug rules (D1) — **extract:** lift verbatim from the current priortool source
   (normalisation, allowed characters, collision handling) into the format spec.
