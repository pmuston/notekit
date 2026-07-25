# notekit Kit Specification — v1

> The Go library that implements the notekit format and runtime mechanics, so that
> each notebook tool (clinote v2 first, then sqlnote etc.) is a thin binary:
> executor + registration + main. Companion to `notekit-format-spec.md` (normative
> for everything on disk) and `notekit-rendering-contract.md` (result kinds).
> Stated as responsibilities and contracts; exact signatures are the implementer's,
> within the constraints below.

Status: draft v1 · Target surface: Go/Echo/HTMX only (harvest D2).

---

## 1. Goals

- One library, several notebook binaries. A new domain notebook is: implement one
  executor, register it, reuse everything else.
- The document model owns format conformance (round-trip, splice, grammar) once,
  centrally, with the format spec's golden corpus as its test suite.
- The server package provides the proven clinote UI mechanics — async run with
  polling spinner, sortable tables, inline prose editing, auto-save — as reusable
  Echo handlers and templates.

## 2. Non-goals

- No UI abstraction beyond good package layering; SwiftUI or other surfaces are
  separate implementations, not consumers (harvest D2).
- No crystallisation, staleness, or PDF machinery in core (harvest D4) — priortool
  remains its own tool; if it later adopts notekit's document model, that is a
  priortool decision.
- No multi-user, auth, CI/headless execution, streaming, or cell chaining.
- No plugin loading; executors are compiled in. Single static binary per tool.

## 3. Package layout and responsibilities

```
notekit/
  doc/       document model: parse, cells, slugs, byte-range splice
  meta/      info-string metadata grammar: parse + canonical serialise
  run/       run scheduler: async execution, capture limits, result splice
  exec/      executor and session contracts (interfaces only)
  kind/      result kinds and renderer registry (see rendering contract)
  serve/     Echo handlers + HTMX templates + embedded assets
```

### 3.1 `doc` — document model

- Parses a notebook per format spec §2–§8: front matter (reserved keys + raw
  passthrough), cell detection, cell identity (stored `id` + derived slug, format spec
  §5), result-block pairing, sidecar references.
- A Markdown parser is used as a **structural scanner only** (harvest P2); the
  package's own byte-range model owns all serialisation. Every construct records its
  exact byte span in the source.
- In practice the scanner is a purpose-built line scanner rather than a goldmark AST
  walk, because goldmark reports no position at all for an info-less, content-less
  fence and reports text rather than line spans for every block. goldmark remains a
  dependency and is used as an independent CommonMark **oracle** in tests, which the
  scanner must agree with. See the implementation plan §3.2 for the evidence.
- Provides splice operations: replace a cell's result blocks, append an `id` to a
  source fence's info string, append a cell, edit a prose range. Splice is the *only*
  write path; there is no "serialise the whole tree" API, by design — that is how
  round-trip identity stays guaranteed.
- Owns `id` assignment and its invariants: lazy (only for cells producing sidecars),
  append-only into the info string, immutable thereafter, duplicates rejected as a
  tool error. The generator is injectable so the corpus stays deterministic.
- Tracks sidecar files by `id`: renames them when a heading (and therefore the slug)
  changes, and reports — never deletes — sidecars whose `id` matches no live cell
  (format spec §5, §8).
- Passes the entire format conformance corpus (format spec §11); the corpus lives
  with this package.

### 3.2 `meta` — metadata grammar

- Implements format spec §9 exactly: parse to an ordered key/value structure,
  duplicate-key rejection, canonical serialisation with reserved-keys-first rule.
- No key semantics. Callers (runtime, executors) interpret.

### 3.3 `exec` — executor and session contracts

The domain boundary. An executor's responsibilities:

- Declare its **language tag** (the info-string tag it claims, e.g. `sh`, `sql`,
  `cypher`, `redis`).
- Own its **session**: opaque state created when a notebook is opened and destroyed
  when it is closed or the process exits. One session per notebook (harvest R1).
  Whether the engine behind the session is internal (embedded store, pty child) or
  external (network database) is invisible above this boundary (harvest D5).
- Execute: given source text, cell metadata (converted from the info string), and
  a context (for cancellation), return a **typed result** — a kind from the
  rendering contract plus its payload — or a typed error carrying the domain's
  message and optional numeric status for the `error` block.
- Honour cancellation and never write to the notebook file itself; persistence is
  `run`'s job through `doc`.

Session teardown must be reliable on SIGINT/SIGTERM — the kit owns the signal
handling and calls every live session's destroy hook.

### 3.4 `run` — scheduler

- Async execution (harvest R3): a run request returns a run ID immediately; state
  is poll-able (running / done / failed). Runs within one notebook are serialised
  in request order — sessions are stateful, so concurrent cell execution within a
  notebook is forbidden. Different notebooks may run concurrently.
- Output capture: 1 MiB cap (harvest R4), ANSI stripping for the durable form,
  truncation marker per format spec §6.
- On completion: builds the `output` or `error` block (canonical metadata: `format`,
  `run`, `tool`, `status`), applies fence-length safety, splices via `doc`, and
  triggers auto-save (harvest R5). Volatile lifecycle: prior result blocks for the
  cell are replaced, and any sidecar files carrying the cell's `id` are overwritten.

### 3.5 `serve` — server and UI mechanics

- Echo handlers and HTMX templates for: notebook view, cell run (post → run ID),
  run status polling at 500 ms with spinner, result rendering via the `kind`
  registry, inline prose editing with unsaved-changes indicator, save.
- Live rendering niceties that must not leak to disk: ANSI colour in the browser,
  sortable tables for `csv`/`jsonl` (JSONL flattened to columns).
- All assets embedded via `go:embed` (harvest R9): no CDN, no network to render,
  no frontend build step. A `make vendor` target may refresh vendored files.
- The package provides components, not a fixed application: each tool composes its
  own `main` (flags, executor registration, session config) around them.

## 4. What a tool binary looks like

Illustrative responsibility split for clinote v2 (its own spec follows as gate 3):

- `main`: flag parsing (`clinote notebook.md`, picker when no arg), shell executor
  construction (pty session, sentinel exit-code capture — all pty knowledge lives
  here and in no kit package), registration, `serve` composition.
- Everything else — format, splice, scheduling, capture limits, UI — is kit.
- Parity test is functional against clinote v1's behaviour, not file-level
  (harvest D8).

## 5. Style constraints (for agentic implementation)

- Go stdlib first. Permitted dependencies: goldmark, Echo, and vendored frontend
  assets (HTMX, Sigma.js/graphology where a tool needs graph rendering). Justify
  anything further in a comment at the import site.
- Single static binary per tool; no runtime file dependencies beyond the notebook
  and its sidecar directory.
- Fail loud: malformed metadata, duplicate keys, refusal of non-notekit files.
  No silent repair of any input (format spec §10).
- Table-driven tests; the format corpus is the acceptance suite for `doc`/`meta`.
- Do not implement anything in any FUTURE section.

## 6. Milestones

- **M0** — `doc` + `meta`: parse, identity (`id` + slug), splice, round-trip green against the full
  conformance corpus. No execution. Deliverable: `notefmt` debug binary (parse,
  list cells, round-trip check) — useful forever as a linter.
- **M1** — `exec` + `run`: contracts plus a trivial `echo` executor; async runs,
  capture, error blocks, truncation, signal-safe teardown. CLI-only demo.
- **M2** — `serve`: full HTMX loop with the echo executor; run, poll, spinner,
  tables, prose edit, auto-save.
- **M3** — clinote v2 built against M2 as the first real consumer; kit changes it
  forces are folded back before any second tool starts.

## FUTURE (recorded, do not implement)

- Streaming execution (contract must not preclude it; kit does not provide it).
- `$LAST_OUTPUT`-style cell chaining.
- Named relations + SQLite join substrate for the polyglot notebook.
- Crystallised lifecycle support in core, if a second crystallising tool appears.
- MCP server exposing notebooks as agent-callable tools.
