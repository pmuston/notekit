# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

Go module `github.com/pmuston/notekit` on Go 1.25.4. **All four harvest validation gates
are met.** The kit (`meta`, `doc`, `exec`, `kind`, `run`, `serve`), the conformance corpus,
and five binaries: `notefmt` (linter), `noterun` and `noteserve` (demos), `clinote` (shell
notebook, at functional v1 parity), and `sqlnote` (SQLite notebook — the non-shell consumer
that gate 4 asked for). The specs are the authority for everything that gets built:

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

### Placeholders: none remain

All four harvest open questions are resolved and their resolutions are in the specs:
the info-string two-axis grammar (1); first-class `error` blocks with interleaved
stdout/stderr (2); the sidecar convention, verified against the priortool *implementation*
at `../priortool` (3); and slug rules, checked against priortool's algorithm by a differential
test (4).

**The sibling tools are readable, and worth reading before reinventing.** `../clinote` is
v1, whose pty runner is the proven prior art the shell executor extracts.
`../priortool` is implemented, and settled §8: its directory name confirmed ours, its payload
JSON showed ours was too thin (offline re-render needs layout, not just data), and its
atomic artifact writes exposed a gap. Its manual *reattach* screen — "the fix for a renamed
heading, whose old artifacts no longer match any cell ID" — is the concrete evidence for
why §5 stores an `id`: that screen is a workaround for the hazard notekit eliminates.

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

Slug rules are cosmetic under this scheme, and they nonetheless match priortool's exactly —
`doc.TestSlugMatchesPriortool` holds priortool's algorithm as an oracle and asserts agreement,
so a notebook migrated from priortool keeps the filenames a reader recognises.

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
  cmd/notefmt/    the M0 linter: check, list, sidecars
  cmd/noterun/    the M1 demo: the full async loop, no server
  cmd/noteserve/  the M2 demo: the full HTMX loop in a browser
  cmd/clinote/    clinote v2 (M3): shell executor + main; ALL pty knowledge lives here
  cmd/sqlnote/    sqlnote (gate 4): SQLite executor + main; ALL database knowledge here
  doc/            document model: parse, cells, slugs, byte-range splice, result writers
  meta/           info-string metadata grammar: parse + canonical serialise
  run/            run scheduler: async execution, capture limits, splice, atomic save
  exec/           executor and session contracts; exec/echoexec is the reference impl
  kind/           result-kind registry: durable writers now, live renderers at M2
  serve/          Echo handlers + HTMX templates + go:embed assets
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
  concurrently. One goroutine per notebook is what enforces this, so `make race` is
  not optional.
- **`Done` vs `Failed` is easy to get backwards.** A cell whose *domain* failed — a
  non-zero exit status — is a **successful run** that persists an `error` block: `Done`,
  with `Form` reporting `error`. `Failed` means the run could not complete at all and
  **nothing is persisted**. Tools should exit 0 on a domain failure.
- **Saving is atomic**: temp file plus rename, preserving the file's mode. The file is
  the artifact, so a torn write is unacceptable.
- **An unregistered result kind is an error**, never a silent fall back to `text` — a
  kind with no durable form is inadmissible.
- **Results are volatile.** Every run replaces the whole of the cell's result position
  and overwrites its sidecar files. No freeze, no staleness, no protection. A run
  writes exactly one result construct — `output` fence, `error` fence, or sidecar
  reference — never a failure folded into degenerate output.
- **Superseded vs orphaned sidecars are different things** (format spec §8.1). A run
  *removes* files carrying the id of the cell it just ran that it did not itself write —
  that cell's own dead artifacts. An orphan, whose id matches no cell at all, is only
  ever *reported*. Never conflate them.
- **Every durable write goes through `doc.WriteFileAtomic`** — the notebook and sidecar
  artifacts alike. A reader must never see a half-written file; for a browser fetching an
  image mid-run that is otherwise exactly what happens. Three hand-rolled copies had
  accumulated before this was centralised, so add call sites rather than new copies.
- **A sidecar `.json` must carry what an offline re-render needs**, not merely the result
  data — for a graph that means node coordinates and camera state too (§8, harvest V4).
  Data alone lays out differently.
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
  form stands alone on GitHub. Concretely: colour lives in `Scheduler.LiveBody`,
  in memory only, and dies with the process — a restart renders without it, and
  `TestColourGoneAfterRestart` guards that. Never persist the pretty version.
- **Live renderers take a *durable body*, not an executor payload.** A server renders
  from disk far more often than from a fresh run, so the payload would force two code
  paths per kind. ANSI conversion is a no-op on already-stripped text, which is what
  makes one renderer serve both.
- **Kinds are looked up by durable `format` value** (`Kind.Formats`, `LookupFormat`),
  because a notebook on disk carries a `format`, not a kind name. `text` claims both
  `""` and `"text"`.
- **Prose edits are addressed symbolically** — `preamble`, `2-before`, `2-after` — and
  never by byte offsets from the client. A stale page's offsets would splice into
  whatever now occupies them, and results move on every run.
- **`run` is not the only writer, so it never caches a parse.** `serve` edits prose,
  sources and structure, and a person may have the notebook open in an editor. A cached
  parse leaves a splice working from offsets that no longer describe the file — and
  because they are still *in range*, it writes to the wrong place instead of failing.
  Every read of the scheduler's parse state re-reads the file first. Do not "optimise"
  this away.
- **A new cell is a heading plus a source fence and nothing else** (§10 f). No invented
  prose, no placeholder result. Creating one tagged `output`/`error` is refused, since
  §4.3 would make it a section with no cell.
- Executors declare whether they emit `csv` or `jsonl`; the kit does **not**
  transcode between them.

## Writing an executor

Two references, deliberately different in shape: `cmd/clinote` (a pty child, text results)
and `cmd/sqlnote` (a database connection, structured results). Read whichever is closer.

Three rules bind every executor. The first two are easy to get wrong because clinote v1 did
the opposite:

- **Return raw output.** ANSI stripping is the *format* layer's job (`doc.StripANSI`, via
  `run`). An executor that strips destroys the colour the browser renders.
- **Bound your own capture, and say so.** Reading a pty into memory unbounded is how a
  runaway cell kills the process; `exec.Capture` helps. Set `exec.Result.Truncated` when
  you drop bytes — `run` cannot know otherwise, and would treat a short body as complete.
- **Per-notebook configuration arrives in `exec.Notebook.Front`**, not on the executor.
  One executor serves many notebooks, and §2 puts a notebook's own settings in its front
  matter (`sqlnote-db`, `clinote-session`). Namespace your keys.

Database-specific lessons from sqlnote:

- **`sql.DB` is a pool, and a session is not.** Hold one dedicated `*sql.Conn` for the
  session's lifetime. For an in-memory database every connection is a *separate empty
  database*, so a pool silently loses everything between cells; even on a file, temp tables
  and PRAGMAs live on the connection. This is what makes harvest R1 true for a database.
- **Do not parse SQL to decide what a cell is.** One `Query` runs every statement and
  returns the columns of whichever produced rows; zero columns means the cell had no final
  `SELECT`. Splitting statements correctly means handling strings, comments and nested
  quoting, and getting it subtly wrong would run the wrong thing.
- **Render values exactly, not prettily.** `7 * 0.05` persists as `0.35000000000000003`.
  csv is data other tools parse, and rounding for looks would silently change the value.

Shell-specific lessons worth not relearning:

- **Do not run the shell interactively.** An interactive shell's line editor owns the
  terminal: zsh's ZLE re-enables echo after `stty -echo` and redraws a prompt before every
  command, and both land in the captured output. Dropping `-i` removes prompt, echo and
  line editor at once; state still carries between cells.
- **Keep reading past the output cap.** The sentinel arrives *after* the output, so a read
  that stops at the cap never sees it and the session desynchronises permanently.
- **`Close` must not take the session mutex.** A hung command holds it, and `Close` runs
  on the Ctrl-C path; closing the pty is what unblocks the stuck read.
- **Guard the pty handle against use-after-close.** The race detector caught an interrupt
  goroutine reading `pty.Fd()` after `Close` — a reused descriptor would have sent SIGINT
  to an unrelated process group.

## Milestones

All complete. Build order, each gated on the previous:

- **M0** — `doc` + `meta`: parse, slugs, splice, round-trip green against the full
  conformance corpus. No execution. Ships `notefmt` (parse, list cells, round-trip
  check) — useful permanently as a linter.
- **M1** — `exec` + `run` with a trivial `echo` executor: async runs, capture, error
  blocks, truncation, signal-safe teardown. CLI only.
- **M2** — `serve`: full HTMX loop — run, poll at 500 ms with spinner, sortable
  tables, inline prose edit, auto-save.
- **M3** — clinote v2 as first real consumer; fold back any kit changes it forces
  before starting a second tool. It forced three: `exec.Result.Truncated`,
  `doc.Cell.SetSource`, and three `serve` routes (cancel, source edit, run-all).

## Testing

The **format conformance corpus** (format spec §11) is the acceptance suite for
`doc` and `meta`, and lives with the `doc` package. It must cover: round-trip
identity over every corpus file; cell detection across mixed cells, inert example
fences, headingless fences, nested heading levels; metadata grammar including
quoting, flags, and duplicate-key rejection; result splice verifying *only* the
expected byte range changed; document edits — prose, source body, section insert and
remove (§11.13); section boundaries and result-position cases (§11.2–3,
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
make race          # go test -race ./... — the scheduler is concurrent
make check-corpus  # lint the acceptance corpus with notefmt itself
make vendor        # refresh HTMX; then update serve/assets/VENDOR.md and read the diff
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

**Harvest R2 now has a home, and it is not the format.** R2 — self-contained versus bound
data modes — was the one proven requirement no spec mentioned. sqlnote answers it as *tool
configuration*: no `sqlnote-db` in front matter means the notebook is self-contained and
runs in memory, so a run from empty reconstructs its data; naming a file binds it to that
store. No format rule was needed, which is why none existed.

All four validation gates (harvest §5) are met: (1) harvest adjudicated, 16 July 2026;
(2) format spec with a golden-file corpus, now free of placeholders; (3) clinote v2 on the
kit at functional v1 parity; (4) sqlnote as a second, non-shell consumer. Gate 4's verdict:
the abstraction held — `doc`, `meta`, `kind`, `run` and `serve` needed no change for SQL —
but it was *incomplete*, and only a second domain could show it. `exec.Open` now receives
front matter, and `doc.Notebook.Front()` exposes the passthrough keys §2 always promised
"exposed to the runtime" but which nothing had implemented.
