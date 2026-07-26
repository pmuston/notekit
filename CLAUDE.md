# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

Go module `github.com/pmuston/notekit` on Go 1.25.4. **M0 is complete**: `meta`, `doc`,
the conformance corpus, and the `notefmt` CLI. `run`, `exec`, `kind`, and `serve` are
doc-comment skeletons — M1 is next. The specs are the authority for everything that gets
built:

| File | Owns |
|---|---|
| [notekit-harvest.md](notekit-harvest.md) | Evidence base: what the three prior tools proved, the adjudications, open questions. Not design — the record of what was already designed three times |
| [notekit-format-spec.md](notekit-format-spec.md) | Normative on-disk format: front matter, cells, result blocks, sidecars, info-string grammar, round-trip policy |
| [notekit-kit-spec.md](notekit-kit-spec.md) | The Go library: package layout, responsibilities, milestones, style constraints |
| [notekit-rendering-contract.md](notekit-rendering-contract.md) | What a result *is*: core result kinds, live vs durable forms, extension rules |
| [notekit-implementation-plan.md](notekit-implementation-plan.md) | Non-normative build plan: staged M0–M3 with gates, testing strategy, and the spec questions each stage forces |

Read the first four in that order — each is downstream of the earlier ones. The plan is
disposable once M3 lands; the specs govern if they ever disagree with it.

### Reading the harvest citations

The specs justify nearly every rule by citing the harvest (`harvest D4`, `harvest P2`,
`harvest F11`). Those are resolvable: **F** = format requirement (§2.1), **R** =
runtime (§2.2), **V** = rendering (§2.3), **P** = implementation posture (§2.4),
**D** = adjudication (§3). When a spec rule looks arbitrary, the citation is the
rationale — follow it before changing anything.

Provenance markers inside the harvest name the source tool: `[cli]` clinote (shell;
Go/Echo/HTMX; the most advanced and the only one actually implemented), `[cy]` graphtool
(Cypher; SwiftUI; spec'd, implementation status unconfirmed), `[cyp]` priortool (Cypher
figure plates; Go/Echo + browser crystallisation to PNG, PDF export; spec'd). A
requirement's provenance tells you how much evidence stands behind it — several rules
rest on clinote alone.

**⚠ in the harvest means inferred from incomplete records** — confirm before relying
on it, don't inherit it silently.

### Unresolved placeholders

One remains: **format spec §8** — sidecar directory name and payload JSON shape,
written from the priortool *spec* with the as-built state unconfirmed (harvest open
question 3). Verify against the priortool implementation and adjust whichever diverged.

Harvest open questions 1, 2, and 4 are resolved and their resolutions are in the specs
(info-string two-axis grammar; first-class `error` blocks with interleaved
stdout/stderr; cell identity — see below).

### Cell identity supersedes harvest D1

**The specs no longer match harvest D1, deliberately.** D1 made a heading-derived slug
the durable identity. That was unsound: its collision rule suffixed duplicate headings
`-2`, `-3` *in document order*, so reordering cells or inserting a cell with a
duplicate heading silently reassigned identity and attached sidecar artifacts to the
wrong cells — no error, no orphan detected, wrong figures rendering until each cell
happened to be re-run. It also contradicted harvest R7's explicit "identity from
content, not position".

Format spec §5 now separates the two jobs that were conflated:

- **`id`** — durable identity. An opaque 8-char base32 token *stored* in the source
  fence's info string, immutable, derived from nothing. Assigned **lazily**: only when
  a cell first produces a sidecar result. Cells with inline results never get one, so
  most fences stay clean. Duplicate `id` in a notebook is a tool error (only reachable
  by copy-paste; fix is to delete the token and let it be reassigned).
- **slug** — a cosmetic name, recomputed on parse, carrying no identity. Bounded at 60
  chars; may be empty (non-ASCII headings are valid, not errors); may be shared by two
  cells, which is not a collision. **No positional suffixes exist anywhere in the
  format.**

Sidecars are `<slug>--<id>.<ext>`, split on the last `--`. Consequences: heading
renames *rename* the sidecar instead of orphaning it; reorders are inert; an orphan now
means exactly one thing — the cell was deleted.

Two knock-on rules that are easy to miss:

- **Running a cell can modify its source fence**, not just its results — appending
  `id`, once per cell lifetime. Format spec §10 enumerates the complete set of
  permitted writes.
- **`id` insertion is append-only** and must not reformat a hand-authored info string:
  existing entries keep their text, spacing, and order. This is an explicit exception
  to §9's reserved-keys-first canonical ordering, which governs tool-written result
  blocks only.

Because slug rules are now cosmetic, aligning them with priortool is nice-to-have rather
than a correctness gate — which is why §5's placeholder is gone while §8's remains.

Harvest §3 is a dated record and was left untouched; this supersedes D1 rather than
rewriting it.

## What notekit is

A notekit notebook is one plain CommonMark file with YAML front matter. The file is
the artifact; any app is merely a runner over it. Notebooks must render faithfully on
GitHub with no app-specific cruft visible, stay grep-able and diff-friendly, and
round-trip byte-identically.

This is a third-time-through design: clinote, graphtool, and priortool each independently
converged on one CommonMark file per notebook, fenced blocks as cells, results
persisted into or beside the file, a single static binary, offline capability, and
plain-text durable artifacts. notekit extracts that convergence into one library so
the next notebook tool is an executor plus a `main`.

The **kit** is a Go library implementing the format and runtime mechanics so each
notebook tool (clinote v2 first, then sqlnote, etc.) is a thin binary: executor +
registration + `main`. Target surface is Go/Echo/HTMX only.

## Architecture (as specified, not yet built)

```
notekit/
  cmd/notefmt/  the M0 CLI: check, list, sidecars — a permanent linter
  doc/    document model: parse, cells, slugs, byte-range splice
  meta/   info-string metadata grammar: parse + canonical serialise
  run/    run scheduler: async execution, capture limits, result splice
  exec/   executor and session contracts (interfaces only)
  kind/   result kinds and renderer registry
  serve/  Echo handlers + HTMX templates + go:embed assets
```

Three architectural decisions drive almost every implementation choice:

**Byte-range splice is the only write path.** A Markdown parser is a *structural
scanner only* — every construct records its exact byte span, and tools rewrite exactly
the byte ranges they executed or edited, copying every other byte through. There is
deliberately no "serialise the whole tree" API; that absence is what guarantees
round-trip identity. Never reflow prose, normalise whitespace, reorder metadata the
tool did not write, or "fix" non-conforming constructs.

**`doc` scans lines itself; goldmark is a test oracle, not the scanner.** The kit spec
said "goldmark as a structural scanner", but goldmark reports *no position at all* for
an info-less, content-less fence, and such a fence is load-bearing — as a section's
first fence it suppresses the cell (§4.3), so missing it would let a tool run the wrong
bytes. Its block spans also cover text, not whole lines. So `doc/scan.go` is a
purpose-built line scanner, and `doc/scan_goldmark_test.go` cross-checks it against
goldmark on every fixture, with one documented divergence (a fence indented inside a
list item). Do not "simplify" the scanner into an AST walk — the probe evidence is in
the implementation plan §3.2.

**The format assigns almost no semantics.** Only `notekit` and `title` in front
matter, and the keys named in format spec §6–§8, are reserved. Everything else —
including *all* keys on source fences — is passthrough: preserved byte-for-byte,
handed to the runtime uninterpreted. `meta` implements grammar with zero key
semantics; callers interpret.

**Parsing is conservative; tools are loud.** A construct that fails to match the
cell/result rules is prose, not an error. Errors come only from tools, and only
about blocks they were asked to run or write. Given that: refuse non-notekit files
rather than guessing, reject duplicate metadata keys, and never silently repair
input.

**The cell model is flat, and read is more permissive than write.** Format spec §4 was
rewritten to settle two ambiguities that blocked `doc`:

- A **section** runs from a heading to the next ATX heading of *any* level. Level is
  irrelevant and **cells never nest.** An earlier draft also had a level-based "span"
  reaching to the next same-or-higher heading, which let one run of bytes be both an
  inner cell's source fence and prose owned by an outer cell. `span` no longer exists;
  don't reintroduce it.
- A cell's **result position** is the region after its source fence covering
  consecutive result constructs. Exactly three forms are admissible: `output` fence,
  `error` fence, sidecar reference. **Read tolerates several, including mixed forms;
  write emits exactly one and replaces the whole region.** The asymmetry is deliberate
  — replacement self-heals, so erroring would only reject documents an older tool
  version legitimately wrote.
- The source fence must be the first fence of *any* kind in its section — so a section
  whose first fence is untagged, or tagged `output`/`error`, contains no cell at all.
  Strict on purpose: no tool should ever have to guess which fence is the source.
- A sidecar reference is the provenance comment **and** image link together. An image
  link with no comment before it is always prose. Users put images in notebooks; this
  rule is what stops a tool overwriting one as though it were a result.

Further invariants worth internalising before touching `run` or `exec`:

- **One session per notebook**, owned by the executor, created on open and destroyed
  on close or process exit. Whether the engine behind it is embedded or a network
  database is invisible above the `exec` boundary. The kit owns signal handling and
  must call every live session's destroy hook on SIGINT/SIGTERM.
- **Runs are async and serialised per notebook.** A run request returns a run ID
  immediately; state is pollable. Sessions are stateful, so concurrent cell
  execution within one notebook is forbidden; different notebooks may run
  concurrently.
- **Results are volatile.** Every run replaces the whole of the cell's result position
  and overwrites its sidecar files. No freeze, no staleness, no protection. A run
  writes exactly one result construct — `output` fence, `error` fence, or sidecar
  reference — never a failure folded into degenerate output.
- **An unclosed source fence has no result position**, so persisting its result is
  refused rather than written (format spec §4.2). The fence extends to end of file;
  appending there lands inside the fence body and corrupts the cell, and closing it
  first would be silent repair. The cell stays readable and runnable. A fuzzer found
  this, not a spec reading — `Cell.SetResult` returns an error for it.
- **Output cap is 1 MiB**, ANSI stripped for the durable form, with the truncation
  marker and `truncated` flag per format spec §6.
- **Fence-length safety:** N backticks where N = max(3, longest backtick run in body
  + 1), for `output` and `error` alike.
- **Live may be prettier, never fuller.** ANSI colour, sortable tables, and graph
  interactivity exist only in the browser and must degrade to nothing; the durable
  form stands alone on GitHub.
- Executors declare whether they emit `csv` or `jsonl`; the kit does **not**
  transcode between them.

## Milestones

Build in this order — each is gated on the previous:

- **M0** — `doc` + `meta`: parse, slugs, splice, round-trip green against the full
  conformance corpus. No execution. Ships `notefmt` (parse, list cells, round-trip
  check) — useful permanently as a linter.
- **M1** — `exec` + `run` with a trivial `echo` executor: async runs, capture, error
  blocks, truncation, signal-safe teardown. CLI only.
- **M2** — `serve`: full HTMX loop — run, poll at 500 ms with spinner, sortable
  tables, inline prose edit, auto-save.
- **M3** — clinote v2 as first real consumer; fold back any kit changes it forces
  before starting a second tool.

## Testing

The **format conformance corpus** (format spec §11) is the acceptance suite for
`doc` and `meta`, and lives with the `doc` package. It must cover: round-trip
identity over every corpus file; cell detection across mixed cells, inert example
fences, headingless fences, nested heading levels; metadata grammar including
quoting, flags, and duplicate-key rejection; result splice verifying *only* the
expected byte range changed; section boundaries and result-position cases (§11.2–3,
including mixed result forms and a bare image link surviving a run); fence-length
safety at 3/4/5-backtick runs; slug
derivation with truncation, empty results, and shared slugs; `id` assignment,
append-only insertion, and duplicate rejection; identity stability across rename,
reorder, and duplicate-heading insertion; orphan reporting; error block form,
truncation marker, ANSI stripping.

The identity-stability cases (format spec §11.8) exist because the pre-`id` scheme
failed them — treat them as regression tests, not hypotheticals. The `id` generator
must be injectable to keep goldens deterministic.

Two layers, and both are wanted. `doc/fixtures_test.go` holds micro-case fixtures
feeding the round-trip test and the goldmark cross-check — add a case there and both
pick it up. `doc/testdata/corpus/` holds realistic notebooks, each with a `.cells`
golden (parsed structure) and a `.spliced` golden (the document after running every
cell). Four walk-driven tests mean dropping in a `.md` file adds all of them:

```bash
go test ./doc -run TestCorpus -update   # regenerate goldens, then review the diff
```

A golden that changed silently is a spec change nobody reviewed, so read the diff.

**Fuzzing is load-bearing here, not decoration.** Byte-identity and append-only
insertion are exactly the properties a fuzzer can falsify, and one already has: the
unclosed-fence corruption above came from `FuzzDocSetResult`, not from review. Six
targets exist across `meta` and `doc`. Splice tests assert *prefix and suffix
identity* rather than a diff span, because "only the expected range changed" is the
real requirement and a diff-based bound is ambiguous for insertions.

Commands:

```bash
make test          # go test ./...
make lint          # go vet + gofmt check
make fuzz          # all six targets, 30s each; FUZZTIME=2m for longer
make check-corpus  # lint the acceptance corpus with notefmt itself
go test ./doc -run TestResultPosition
```

`notefmt` exits 0 (clean), 1 (problem found), or 2 (usage or I/O failure). A refused
non-notebook is a finding about a file, not a crash, so one bad file in a glob does not
abort the run. Errors are spec violations (refusal, duplicate `id`, malformed
metadata); warnings are things that are legal but worth saying (unclosed fence,
several result constructs, stale or orphaned sidecars). `-strict` promotes warnings.
**notefmt never writes to a notebook** — a stale sidecar is reported with the name it
should have, and an orphan is reported and left alone.

## Dependency policy

Go stdlib first. Permitted: **goldmark**, **Echo**, and vendored frontend assets
(HTMX, Sigma.js/graphology where a tool renders graphs). Anything further must be
justified in a comment at the import site. All web assets embedded via `go:embed` —
no CDN, no network to render, no frontend build step. Single static binary per tool,
with no runtime file dependencies beyond the notebook and its sidecar directory.

Per the user's global preferences: if SQLite is needed (e.g. the sqlnote tool), use
`modernc.org/sqlite`, not `github.com/mattn/go-sqlite3`.

## Scope discipline

Each spec ends with a **FUTURE** section. Do not implement anything in one — they are
recorded to keep the contracts from precluding them, nothing more: streaming
execution, `$LAST_OUTPUT` cell chaining, named relations / SQLite-join substrate,
crystallised lifecycle keys, an MCP server, `relation` as a first-class kind.

Also explicitly out of scope for v1: clinote v1 file compatibility, cross-cell
references, multi-notebook includes, execution order beyond document order, rich-text
prose constructs, multi-user/auth, CI/headless execution, read-only iPad viewer,
binary result kinds beyond the sidecar image pattern, plugin loading (executors are
compiled in), and any crystallisation, staleness, or PDF machinery — that stays inside
priortool.

**Proven ≠ in scope.** Three requirements are proven in a shipped or spec'd tool yet
deliberately excluded from kit core by adjudication D4: staleness detection (R6),
browser-side crystallisation (V3), and the heading-SHA-256 identity scheme (R7's
mechanism, superseded — though R7's *principle*, identity from content rather than
position, is upheld; see cell identity above). Finding one of these in priortool is not a
reason to build it here.

One proven requirement has **no home in any current spec**: harvest R2, self-contained
vs bound data modes — whether a notebook defines its own data (run-all from empty
reconstructs it) or references an external store. The harvest calls this a general
property rather than a Cypher quirk, but neither the kit spec nor the format spec
mentions it. Worth raising rather than quietly dropping when the specs are next
revised.

Validation gates run in order (harvest §5): (1) harvest adjudicated — done, 16 July
2026; (2) format spec drafted with a golden-file conformance corpus — the current
gate; (3) clinote v2 on the kit at functional v1 parity; (4) only then a second,
non-shell consumer to test the abstraction.
