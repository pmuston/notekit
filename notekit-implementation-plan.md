# notekit Implementation Plan — v1

> How the kit gets built. Operationalises the kit spec's M0–M3 (`notekit-kit-spec.md`
> §6) into ordered stages with acceptance gates, names the sequencing constraints the
> milestone list glosses over, and lists the spec questions each stage will force.
> Not normative: the three specs govern. This document is disposable once M3 lands.

Status: draft · **M0–M3 complete, every spec question resolved, and validation gate 4 met**
by sqlnote — the first non-shell consumer. No placeholders remain in any spec. This document
is now disposable per its own preamble; the specs govern.

---

## 1. Sequencing principles

- **Dependency order, not feature order.** `meta` before `doc` before `run` before
  `serve`. Nothing is built before the thing it validates against exists.
- **The corpus is the contract.** For `doc` and `meta` the golden-file corpus (format
  spec §11) *is* the acceptance suite. Authoring corpus files is design work, not test
  chores — expect it to surface spec ambiguities (§8 below) and budget spec revision
  inside M0 rather than after it.
- **One invariant dominates:** parse → serialise is byte-identical. Every stage adds
  writes; every stage re-proves the invariant over the whole corpus. It is never
  "done".
- **Fail loud, no silent repair** (kit spec §5). When in doubt during implementation,
  error rather than guess — the format is conservative about *parsing* (unmatched
  constructs are prose) and strict about *tool operations*.
- **Nothing from any FUTURE section.** Three FUTURE lists exist; they are recorded so
  contracts don't preclude them, not as backlog.

## 2. Stage 0 — repo bootstrap

Small but currently blocking: there is no module and no git repository.

1. `git init`; initial commit of the four existing spec documents plus `CLAUDE.md`
   before any code, so the specs are the repo's first history.
2. `go.mod`, module path `github.com/pmuston/notekit`. Pin the Go version to whatever
   the local toolchain reports rather than assuming a release.
3. `.gitignore` — built binaries (`notefmt`, later tool binaries), `*.assets/` scratch
   from manual testing.
4. `Makefile` with `build`, `test`, `fuzz`, `lint`, and the `vendor` target the kit
   spec anticipates for refreshing vendored frontend assets. Keep it thin; `go test
   ./...` should always work directly.
5. Empty package skeletons with doc comments only: `doc/`, `meta/`, `run/`, `exec/`,
   `kind/`, `serve/`. Each package's doc comment cites the spec section it implements.
6. CI (GitHub Actions): `go vet`, `go test ./...`, `gofmt -l` check. Add the fuzz
   targets to CI as short-duration smoke runs once they exist (§7).

**Gate:** `make test` passes on empty packages. Specs committed. No implementation yet.

## 3. M0 — `meta` + `doc`

The deliverable is a parser that can round-trip anything and splice precisely, plus
`notefmt` as a permanent linter. No execution.

### 3.1 M0a — `meta` (format spec §9)

No dependencies; build first. Responsibilities:

- Parse an info string into an **ordered** key/value structure preserving each entry's
  original source text (needed for append-only rewriting, format spec §9).
- Distinguish bare values, quoted values with `\"` / `\\` escapes, and valueless keys
  (boolean flags, true).
- Reject duplicate keys as an error.
- Canonical serialisation: single spaces after commas, reserved keys first in §6/§7
  order, then passthrough keys in original order.
- **Append-only insertion** of a single entry, for `id` (format spec §5.1, §9):
  existing entries keep their text, spacing, and order byte-for-byte. Handle the
  no-metadata case (`sh` → `sh {id=…}`) and the has-metadata case (`{format=csv}` →
  `{format=csv, id=…}`).
- No key semantics whatsoever. `meta` must not know what `id` or `format` mean.

**Gate:** ✅ met. Table-driven tests over valid and invalid info strings; parse →
serialise identity for every well-formed input; 100% statement coverage; three fuzz
targets green (`FuzzMetaRoundTrip`, `FuzzMetaInsertIsAdditive`, `FuzzMetaFormatParse`).

Design note: `Format` takes the reserved-key order as a **parameter** rather than
holding §6/§7's orders itself. That is what keeps the package free of key semantics —
those orders belong to whichever package writes result blocks, i.e. `run` at M1.
Similarly `Entry.Quoted` records whether a value was quoted in the source, so a tool
can match the source's style where the format permits either form.

### 3.2 M0b — `doc` (format spec §2–§8) — ✅ COMPLETE

Depends on `meta`. Build in this internal order, because each step's tests need the
previous:

1. **Front matter.** Split on `---` delimiters. Extract only `notekit` (must be `1`)
   and `title`; treat the remainder as an **opaque byte range**, not parsed YAML.
   Refuse any file lacking front matter or `notekit: 1`. *Deliberately not*
   round-tripping YAML through a marshaller — that is the simplest way to guarantee
   §2's byte-for-byte passthrough, and it means no YAML dependency beyond reading two
   scalars.
2. **Structural scan.** Every construct records its exact byte span; nothing in `doc`
   serialises a tree. **Deviation from kit spec §3.1, forced by evidence:** the scanner
   is a purpose-built line scanner, not a walk of goldmark's AST. Probing goldmark 1.8.4
   showed it reports an info-less, content-less fence (```` ```\n``` ````) as a node with
   *no position information at all*, so that fence's span cannot be recovered — and such
   a fence matters, because as a section's first fence it suppresses the cell (§4.3), so
   missing it would let a tool run the wrong bytes. Block spans also cover text rather
   than whole lines (a `## H` heading reports only `H`), so line-snapping was needed
   regardless, and fence-interior tracking — "is this heading inside a fence?" — falls
   out of a line scan for free while an AST walk would need the spans goldmark cannot
   give. goldmark stays in the build as an independent CommonMark **oracle**:
   `scan_goldmark_test.go` asserts the scanner agrees with it on every fixture, with one
   documented divergence (a fence indented inside a list item). Harvest P2's intent — a
   Markdown parser as structural scanner, never as serialiser — is upheld; only the
   choice of which code reads the bytes changed. Worth folding into kit spec §3.1.
3. **Cell detection** per §4: ATX heading 2–6, optional prose, and a source fence that
   is the first fence of *any* kind in the section (§4.1 — section runs to the next
   heading of any level; cells never nest). Non-first language fences are inert
   examples; a section whose first fence is untagged or tagged `output`/`error` holds
   no cell.
4. **Slug derivation** per §5.2: lowercase, non-`[a-z0-9]` runs → `-`, trim, truncate
   to 60 then re-trim, empty permitted. Shared slugs are legal and get no suffix.
5. **`id` handling** per §5.1: read from the source fence; assign lazily via `meta`'s
   append-only insert; reject duplicates within a notebook; injectable generator.
6. **Result position** per §4.2: the region after the source fence spanning consecutive
   result constructs, blank lines permitted. Three admissible forms — `output` fence,
   `error` fence, sidecar reference (provenance comment + image link, §8). Read
   tolerates several and mixed forms; write emits exactly one and replaces the region.
   A bare image link with no provenance comment is prose and must survive a run
   untouched.
7. **Sidecar bookkeeping** per §8.1 — done, and with **no filesystem dependency**.
   `ClassifySidecars` is a pure function from a directory *listing* plus the notebook's
   cells to a per-file verdict: current, stale (heading renamed — rename the file to
   `Want`), orphan (id matches no cell — report, never delete), or foreign (no valid id,
   so notekit did not write it). The caller supplies the listing and performs any
   rename, which keeps the classification trivially testable and leaves `os` out of
   `doc` entirely. M0d's job shrinks to `os.ReadDir` plus printing.
8. **Splice operations**, the only write path: replace a cell's result blocks, append
   an `id` to a source fence, edit a prose range, append a cell. Include the prose-range
   splice now even though nothing exercises it until M2 — it is the same machinery.

### 3.3 M0c — the conformance corpus — ✅ COMPLETE

Lives at `doc/testdata/corpus/`: 11 realistic notebooks, each with two goldens
regenerated by `go test ./doc -run TestCorpus -update`.

- `NAME.cells` — the parsed structure: cells, slugs, ids, closed state, result position
  bytes, and per-result form and metadata. A change in cell detection, result pairing,
  slug derivation, or id reading shows up as a reviewable diff.
- `NAME.spliced` — the whole document after replacing every cell's result in one
  `Apply`, plus a trailing report of any cell whose result could not be persisted. This
  exercises the write path over the entire corpus at once.

Four walk-driven tests cover §11.1 (round-trip), §11.2–3 and §11.7–8 (via `.cells`),
§11.5 (via `.spliced`, plus a convergence check that splicing twice is a fixed point),
and §11.7 again by assigning an id to every cell that lacks one. Adding a `.md` file
adds all of them.

The in-code fixtures in `doc/fixtures_test.go` remain: they are micro-cases feeding the
round-trip test and the goldmark cross-check, where the corpus files are realistic
documents exercising combinations. Both are wanted.

Original intent, retained for reference — cover all §11 items, the ones that matter
most being those that encode decisions rather than mechanics:

- round-trip identity over every corpus file, driven by a directory walk so adding a
  file automatically adds a test;
- cells mixed with inert example fences, fences with no heading, nested heading levels;
- metadata grammar: quoting, flags, duplicate rejection;
- result splice touching *only* the expected byte range (assert on byte offsets, not
  just output equality);
- fence-length safety at 3-, 4-, and 5-backtick bodies;
- slug truncation, empty slug from a non-ASCII heading, two cells sharing a slug;
- `id` appended to fences with and without existing metadata, leaving all other bytes
  identical; duplicate `id` rejected; no `id` assigned to an inline-result cell;
- **identity stability (§11.8) — regression tests, not hypotheticals.** Heading rename
  keeps attachment and renames files; reorder and duplicate-heading insertion change
  no attachment; unmatched sidecar reported and retained. These are the cases the
  pre-`id` scheme silently failed.

### 3.4 M0d — `notefmt` — ✅ COMPLETE

Three subcommands at `cmd/notefmt`: `check` (round-trip, format errors, sidecar state),
`list` (one line per cell), `sidecars` (classify the sidecar directory).

Exit status splits three ways rather than two, which is what makes it usable as a CI
gate: 0 nothing wrong, 1 a problem was found, 2 a usage or I/O failure. A refused
non-notebook is a *finding* about a file (1), not a crash (2) — so one bad file in a
glob does not abort the run or mask the rest.

Errors and warnings are distinguished, and the split is a spec distinction, not a
severity guess:

- **errors** — a refused file, a duplicate `id`, malformed info-string metadata on a
  source fence or a result block, a round-trip failure. All of these are tool errors
  under §9 or §2.
- **warnings** — an unclosed source fence (the cell is readable and runnable, but its
  result can never be persisted, §4.2), several result constructs in one position
  (legal on read; the next run collapses them, §4.2), and stale or orphaned sidecars
  (§8.1). `-strict` promotes warnings to problems.

notefmt never writes to a notebook. Reporting and repairing are different jobs and only
the second needs a tool that can damage a file — so a stale sidecar is reported with the
name it *should* have, and an orphan is reported and left alone.

**Gate:** ✅ met. The full corpus is green; `notefmt check` over the corpus produces
zero errors and exactly the three warnings those files exist to demonstrate (`make
check-corpus`, also run in CI); six fuzz targets green; `doc` 99.8%, `meta` 100%,
`notefmt` 95% statement coverage.

`TestSpecExamplesConform` closes the other half of the gate — "every spec document's
embedded examples". It extracts each ````markdown block from the specs, wraps it in
front matter, and asserts it parses, round-trips, and contains no malformed metadata.
That is a real guard, not decoration: it was verified to fail when §8's old
space-separated provenance example is reintroduced, which is precisely the drift a
reader would otherwise copy into a tool.

**M0 gate:** full corpus green; `notefmt` round-trip-checks every corpus file and every
spec document's embedded examples; both fuzz targets green. No execution code exists.

## 4. M1 — `exec` + `run` + `kind` (durable half) — ✅ COMPLETE

**Sequencing correction to the kit spec:** §6 implies `kind` arrives with `serve`, but
`run` cannot write a result block without knowing its durable form — whether a result
is `text`, `csv`, `jsonl`, or a sidecar kind. So `kind`'s **durable writer** registry
lands in M1; only its **live renderer** half waits for M2. Build the registry with both
slots from the start and leave the renderer slot empty.

- **`exec`** — interfaces only: language tag declaration; session lifecycle tied to
  notebook open/close; `Execute(ctx, source, cellMeta) (Result, error)` returning a
  typed kind + payload, or a typed error carrying message and optional numeric status.
  Engine locality never surfaces here (harvest D5). Executors never touch the notebook
  file.
- **`run`** — run IDs returned immediately; pollable state (running/done/failed); runs
  **serialised per notebook** (one worker per notebook, FIFO queue) because sessions are
  stateful; concurrent across notebooks. 1 MiB capture cap, ANSI stripping for the
  durable form, truncation marker plus `truncated` flag. On completion: build the
  `output` or `error` block with canonical metadata (`format`, `run`, `tool`, `status`),
  apply fence-length safety, splice via `doc`, auto-save.
- **Signal-safe teardown** — the kit owns SIGINT/SIGTERM and calls every live session's
  destroy hook. Test this properly; it is the kind of thing that silently rots.
- **`echo` executor** — trivial, returns its input. The point is to exercise the
  contract, including a deliberate failure path for `error` blocks.

**Gate:** ✅ met. `noterun` drives the full async loop through the echo executor; the
corpus is still green; error blocks, truncation, and cancellation are covered; teardown is
verified by actually sending SIGINT and SIGTERM to the test process, not by inspection.
`go test -race ./...` is clean and runs in CI, because one goroutine per notebook makes
data races a real failure mode rather than a theoretical one.

Design notes worth carrying into M2:

- **Done versus Failed** is the distinction most likely to be got backwards. A cell whose
  *domain* failed — a non-zero exit status — is a **successful run** that persists an
  `error` block: `Done`, with `Form` reporting `error`. `Failed` means the run could not
  complete (no session, a refused splice, an unwritable file) and **nothing is
  persisted**. `noterun`'s exit code follows: a domain failure exits 0.
- **An unclosed source fence is refused before execution, not after.** The cell has no
  result position (§4.2), so running it and then discarding the answer would waste the
  side effects of a real command.
- **Saving is atomic** — temp file plus rename, preserving the notebook's mode. A crash
  midway through a direct write would leave a truncated notebook, and the file is the
  artifact. A concurrent reader never observes a partial parse, which
  `TestSaveIsAtomic` asserts by polling the file while runs are in flight.
- **An unregistered kind is an error**, never a silent fall back to text: a kind with no
  durable form is inadmissible under the two-forms rule.
- `Scheduler` takes an injectable clock and id generator for the same reason the corpus
  does — result metadata carries a timestamp, and a sidecar run may assign an id.

## 5. M2 — `serve` — ✅ COMPLETE

Echo handlers plus HTMX templates, all assets `go:embed`-ed, no CDN, no frontend build
step.

- Notebook view; cell run (POST → run ID); status polling at 500 ms with spinner;
  result rendering through the `kind` live registry.
- Live-only niceties that must not reach disk: ANSI colour in the browser, sortable
  tables for `csv`/`jsonl` with JSONL flattened to columns by first-seen key order.
- Inline prose editing with an unsaved-changes indicator, and save — exercising the
  prose-range splice built in M0b.
- Components, not an application: each tool composes its own `main` around them.

**Gate:** ✅ met, and verified in a real browser rather than only by assertion — clicking
Run showed the spinner, then the result with live colour and provenance; the csv cell
rendered as a sortable table with server-rendered rows.

Design notes:

- **The live renderer takes a durable body, not an executor payload.** A server renders a
  notebook read from disk far more often than a fresh run, so if the payload were the
  input every kind would need two code paths. Freshness is expressed by *which* body the
  caller passes: `Scheduler.LiveBody` for a result this process produced, which still
  carries ANSI, or the persisted body, which does not. One renderer, and ANSI conversion
  is a no-op on already-stripped text.
- **Colour is in-memory only, and dies with the process.** That is the contract, not a
  limitation: the durable form is the plain form (harvest F12), so a restart renders
  without colour. `TestColourGoneAfterRestart` asserts it, because "prettier, never
  fuller" is easy to violate by quietly persisting the pretty version.
- **Kinds are looked up by durable `format` value**, not by kind name, because that is
  what a notebook on disk carries. `Kind.Formats` makes the reverse mapping explicit, and
  `text` claims both `""` and `"text"` since absent and `text` mean the same thing (§6).
- **Prose references are symbolic** (`preamble`, `2-before`, `2-after`), never byte
  offsets from the client. A stale page holding byte offsets would splice into whatever
  now occupies them — and results move on every run. Recomputing from the current parse
  means a stale reference addresses the wrong *region* at worst, never the middle of a
  fence.
- **`serve` never caches a parse.** A run rewrites the file, so a cached parse is stale
  the moment anything runs. The file is the artifact; reading it is the honest way to
  know what it says.

## 6. M3 — clinote v2 — ✅ COMPLETE

First real consumer, and the test of whether the kit's boundaries are right.

- `main` owns: flags (`clinote notebook.md`, picker when no arg), pty shell executor,
  sentinel exit-code capture, registration, `serve` composition. **All pty knowledge
  lives here and in no kit package.**
- Parity with clinote v1 is **functional, not file-level** (harvest D8) — v1 files need
  not open.
- Fold any kit changes M3 forces back into the kit *before* a second tool starts
  (validation gate 4).

### What M3 forced back into the kit

The point of building a real consumer was to find these, and it did.

- **`exec.Result.Truncated`.** An executor reading from a pty must bound memory before
  the runtime sees a byte, and having dropped output only it knows. Without the field an
  executor capping at exactly the durable cap would hand `run` a body it considered
  complete, silently losing the fact that output was lost. `run` now ORs it with its own
  cap.
- **`doc.Cell.SetSource`.** Editing a cell's source is a core notebook operation and
  fence-length safety is format knowledge, so it belongs in `doc`. It re-emits the whole
  fence rather than only the body: a body containing a backtick run at least as long as
  the fence would terminate it early and turn the rest of the cell into prose. Widening
  is not silent repair — §10 permits rewriting the bytes a tool edited, and the fence is
  what is being edited.
- **Three `serve` routes** — cancel a run, edit a cell's source, run all cells. All three
  are kit features every tool needs, and all three were missing only because the echo
  executor never made their absence hurt.

### What clinote v1 taught, and what inverted

v1's runner is at `../clinote/internal/runner`. Carried over as-is, because each detail
is load-bearing and none is obvious:

- A rolling tail window for sentinel detection, kept separate from the captured body, and
  reading that **continues past the cap**. Truncation must never hide the sentinel, or the
  next cell reads the previous cell's marker and the session is permanently out of step.
- `Close` deliberately not taking the session mutex. A command blocked on input leaves
  Execute holding it, so a Close that waited would deadlock — and Close runs on the Ctrl-C
  path. Closing the pty is what unblocks the stuck read.
- `Interrupt` via `TIOCGPGRP` then `kill(-pgrp, SIGINT)`.

Two of v1's responsibilities inverted, because the kit owns them now:

- **v1 stripped ANSI in the runner.** notekit strips in the format layer, so the executor
  must return **raw** output or live colour has nothing to render.
- **v1 split stderr to a temp file** via a `2>` redirect. §7 requires shell stdout and
  stderr combined and interleaved, and the format has no place for two streams — so the
  redirect and the temp file both go away. Simpler, and what the spec asks for.

One thing v1 did that v2 must not: run the shell **interactively**. An interactive shell
runs a line editor that owns the terminal — zsh's ZLE re-enables echo after `stty -echo`
and redraws a prompt before every command, and both land in the captured output. Only
running it end to end showed this; dropping `-i` removes the prompt, the echo, and the
line editor at their source. Nothing a notebook wants is lost, because state still carries
between cells.

### Parity: honest status

Gate 3 is *functional* parity with v1, not file-level (harvest D8). Comparing v1's route
surface, v2 now matches it: run, run-all, cancel, result rendering with live colour and
sortable tables, prose editing with an unsaved indicator, source editing, add a cell,
delete a cell, auto-save, output cap with truncation, exit-status capture, embedded assets.

One deliberate difference remains: v1 had a **web picker**, v2 has a CLI one. For a tool
that serves a single notebook per process, choosing before the server starts is the
simpler place to choose.

### Add and delete: what they forced

Deferring these was right — each raised something a hasty version would have got wrong.

- **§10 was out of date**, and my own M3 work had made it so: the list of permitted writes
  named results, sidecars, `id` and prose, but not source-body edits. It is now grouped
  into writes a *run* makes and writes a *user* asks for, and includes both source edits
  and section insert/remove. The grouping is the point — it explains why the list is
  closed rather than arbitrary.
- **A new cell is a heading plus a fence and nothing else.** A tool that also invented
  prose or a placeholder result would be writing content the user did not ask for into a
  file whose whole premise is that the user owns it.
- **Creating a cell tagged `output` or `error` is refused.** §4.3 says a section whose
  first fence carries a result tag contains no cell, so the tool would be creating a cell
  that is not one.
- **Append-then-delete is not byte-identical**, and should not be. Append seats the new
  heading with a blank line above it, which becomes part of the *previous* section — so
  deleting the new cell correctly leaves it alone and the file ends one newline longer.
  Absorbing it would mean a delete reaching outside the section it was asked to remove.
  `TestAppendThenDeleteRestoresEveryCell` records what actually holds.
- **The scheduler was caching its parse**, which the add-cell work exposed as a
  corruption bug rather than a staleness annoyance. `run` is not the only writer: `serve`
  edits prose, sources and structure, and a person may have the notebook open in an
  editor. A cached parse leaves a splice working from byte offsets that no longer describe
  the file — and because the offsets are still *in range*, it writes to the wrong place
  instead of failing. Every read of the parse state now re-reads the file first, which
  costs one read per run against executing a command. Two tests cover it, including bytes
  inserted *above* the cell being run so every offset below shifts.
- **The new-cell tag comes from the scheduler**, not from a `serve` option. A tool that
  forgot to set it would silently offer to create cells nothing could run.

## 6a. Gate 4 — sqlnote, the first non-shell consumer — ✅ COMPLETE

Harvest validation gate 4: "a second consumer (sqlnote or a graphtool successor) to test the
abstraction against a non-shell domain." A SQLite notebook, using `modernc.org/sqlite` per
the project's driver policy — pure Go, so the binary stays a single static file.

**The abstraction held.** `doc`, `meta`, `kind`, `run` and `serve` needed no change to
accommodate SQL. Everything domain-specific is 300 lines in `cmd/sqlnote/sql.go`. The one
contract change was real but small, and it is a gap rather than a mismatch — see below.

### The one thing gate 4 forced

**`exec.Executor.Open` now receives the notebook's front matter**, as an `exec.Notebook`
carrying path, title and passthrough scalars. A shell executor does not care which notebook
it serves; a database executor cannot start without knowing *which database*, and §2 puts
that in front matter. One executor serves many notebooks, each possibly naming a different
database, so configuring the executor would not do — it has to arrive per-session.

Underneath that sat a **conformance gap nothing had noticed**: §2 says passthrough keys are
"exposed to the runtime uninterpreted", and `doc` exposed none of them. `Notebook.Front()`
now does, reading indent-zero scalars the same way the reserved two are read. Nested blocks
appear with an empty value — enough to know they are there, not enough to misread them —
and the block itself stays an opaque byte range, which is what keeps §2's byte-for-byte
round-trip free of a YAML marshaller.

That is a satisfying result for the gate: the abstraction did not leak, but it *was*
incomplete, and only a second domain could show it.

### Harvest R2 finally has a home

R2 — self-contained versus bound data modes — was the one proven requirement with no home
in any spec, flagged in CLAUDE.md as "worth raising rather than quietly dropping". sqlnote
answers it, and the answer is that it belongs to **tools, not the format**:

- No `sqlnote-db` in front matter → the notebook is **self-contained**. It runs in memory,
  its cells build the data, and a run from empty reconstructs it. Nothing is written beside
  the notebook.
- `sqlnote-db: ./analysis.db` → the notebook is **bound** to that store, resolved against
  the notebook's own directory so it means the same wherever the tool was launched.

No format rule was needed, which is why no spec mentioned it. R2 is real, and it is
configuration.

### What a non-shell domain does differently

- **Results arrive structured.** A `SELECT` returns rows and columns, so `kind.Table` is
  reached without the executor formatting anything by hand — the first genuine exercise of
  that path, since the shell only ever produced text or hand-rolled csv. csv is the default
  because the durable form is what a reader sees on GitHub; `{format=jsonl}` is there for
  when NULL must stay distinct from an empty string, which csv cannot express.
- **Errors carry engine codes.** SQLite's result codes map straight onto
  `exec.Error.Status`: a syntax error is 1, a unique-constraint violation is the extended
  code 1555. Both persist as first-class `error` blocks (§7).
- **The session is a connection, and that is load-bearing.** `sql.DB` is a *pool*, and for
  an in-memory database every connection is a **separate empty database** — a pool would
  silently lose everything between cells. Even on a file, temp tables and PRAGMAs live on
  the connection. Holding one dedicated `*sql.Conn` for the session's lifetime is what makes
  harvest R1's "state carries between cells" true for a database, and a test proves it with
  a temp table read from a later cell.
- **One `Query` serves both cell shapes.** The driver runs every statement in the text and
  returns the columns of whichever produced rows; a cell with no final `SELECT` comes back
  with zero columns. That is how the two are told apart *without parsing SQL* — splitting
  statements correctly means handling strings, comments and nested quoting, and getting it
  subtly wrong would run the wrong thing.
- **Float rendering is exact, not pretty.** `7 * 0.05` persists as `0.35000000000000003`,
  the shortest representation that round-trips. It looks like a bug and is not: csv is data
  other tools parse, and rounding for looks would silently change the value. The `sqlite3`
  CLI prints fewer digits; a notebook's durable form should not.

## 7. Testing strategy

Beyond the corpus and table-driven unit tests:

- **Fuzzing is unusually well-suited here.** Byte-identity is a perfect fuzz property.
  Two native Go targets: `FuzzMetaRoundTrip` (info string → parse → serialise, assert
  identity for well-formed input, assert no panic for anything) and `FuzzDocRoundTrip`
  (arbitrary bytes → parse → serialise, assert identity or clean refusal, never panic
  and never partial output). Seed both from the corpus. Run short in CI, long locally
  before any tag.
- **Byte-offset assertions, not just value equality**, for every splice test. "Only the
  expected range changed" is the actual requirement; equality of the result can pass
  while the tool has rewritten and coincidentally reproduced neighbouring bytes.
- **Injectable `id` generator** so goldens are deterministic. Also inject the clock —
  `run` stamps RFC 3339 timestamps into result metadata, which otherwise makes every
  golden unstable.
- **Property test for slug/`id` independence:** for any corpus notebook, permuting cell
  order or rewriting headings must leave the `id`→sidecar mapping unchanged.

## 8. Spec questions implementation will force

Not blockers for Stage 0, but each must be settled before the stage that hits it.
Recording them here so they get resolved deliberately rather than by whichever
implementation choice happened first.

### 8.0 Unclosed source fences — ✅ RESOLVED, format spec §4.2

Found by `FuzzDocSetResult`, not by reading the spec. An unclosed fence runs to end of
file, so a cell's result position does not exist: appending a result lands *inside the
fence body* and silently corrupts the cell. Resolved by refusing — the cell stays
readable and runnable, only persisting its result is impossible until the author closes
the fence. Auto-closing would be the silent repair §10 forbids. `Cell.SetResult`
therefore returns an error, and `TestSetResultRefusesUnclosedFence` is the regression.

### 8.1 "Section" vs "span" in §4 — ✅ RESOLVED, format spec §4.1

`section` is now the single delimiting concept, running to the next ATX heading of
**any** level; the level-based `span` is deleted. Cells never nest. The old pairing —
"section" for fence detection, level-based "span" for result pairing — let one run of
bytes be both an inner cell's source fence and prose belonging to an outer cell, with no
rule to decide which claim won. Result pairing now depends only on adjacency to the
source fence (§4.2), never on section size, so flattening costs nothing.

### 8.2 One result *or* one sidecar reference — ✅ RESOLVED, format spec §4.2

**Result position** is defined once, admitting exactly three forms: `output` fence,
`error` fence, or sidecar reference. Asymmetric read/write rules:

- **read** tolerates several constructs, including mixed forms — a notebook may carry
  them from an older tool version or a hand edit, and all belong to the cell;
- **write** produces exactly one, replacing the whole region.

Tolerance beats erroring here because a run replaces the entire region, so the condition
self-heals; erroring would reject documents a previous tool version legitimately wrote.

A third ambiguity surfaced while resolving these and is settled in §8 alongside them:
the provenance comment and image link form **one** construct, and an image link with no
comment before it is *always* prose. Users put images in notebooks; without that rule a
tool could overwrite one as if it were a result.

### 8.3 Sidecars stranded by a kind change — ✅ RESOLVED, format spec §8.1

A run removes files carrying the id of the cell it ran that it did not itself write.
That is the volatile lifecycle applied to sidecars: a run replaces the whole of a cell's
result, so a previous artifact of the same cell is dead once the run completes — whether
the cell now produces different files or no sidecar at all.

Neither option originally floated was right. Report-only leaves cruft accumulating with
nobody to clear it; "delete on volatile replacement" as stated was too broad, because it
did not distinguish whose artifact was being removed. The distinguishing question is
ownership: a superseded file belongs to the cell the user just asked to run and would
have been overwritten anyway had its name not changed, while an orphan belongs to a cell
that no longer exists and is only ever reported. `TestSupersededSidecarsRemoved` asserts
both halves in one run.

### 8.3 (original wording, retained for context) — sidecars stranded by a kind change

A cell that produced a sidecar (and so carries an `id`) is edited to produce text
instead. Volatile lifecycle overwrites *result blocks*, and the rendering contract says
re-running overwrites sidecar files — but nothing overwrites a sidecar that the new run
doesn't produce. The old files linger, and they are **not** orphans by §8.1's
definition, because their `id` still matches a live cell. Options: treat "sidecar files
under a live `id` not written by the latest run" as a reportable stale state, or have
`run` delete them as part of volatile replacement. The second contradicts "never delete
silently"; the first is probably right and needs §8.1 wording.

### 8.8 Provenance comment attribute separator — ✅ RESOLVED, format spec §8

§8 said the comment's attributes use "the §9 metadata grammar without braces" while its
own example separated them with spaces alone — and §9's grammar is comma-separated. The
two could not both be right. Resolved in favour of commas: one grammar means one parser
(`meta.Parse` on `"result {" + attrs + "}"`, no extra code) and one canonical writer,
and the comment is invisible in rendered output so the punctuation costs nothing. The priortool
verification (§8.7) has since confirmed the surrounding section without disturbing this
choice.

Also corrected while implementing: the id used as an example throughout the specs,
`a7f3k2p9`, is not valid base32 — it contains `9`, and §5.1's alphabet is `[a-z2-7]`.
The canonical example is now `k3m7q2vf`. Keeping the alphabet and fixing the example
was the right way round: base32 excludes `0 1 8 9` precisely to avoid confusion with
`o l b g` in a token humans occasionally read aloud or retype.

### 8.4 Truncation and fence length ordering — ✅ RESOLVED, format spec §6

Truncate, append the marker, then compute fence length. Enforced by composition rather
than by convention: `doc.Truncate` returns the truncated body and `ResultBlock.String`
computes the fence from whatever body it is handed, so the order cannot be got wrong
without deliberately recombining them. Pinned by a test that truncates inside a
five-backtick run and asserts the result still re-parses as one result.

### 8.5 ANSI stripping scope — ✅ RESOLVED, format spec §6

CSI sequences, OSC sequences, and any other `ESC` sequence are removed; remaining
control characters are dropped except tab and newline, so CRLF becomes LF and a
progress-bar CR disappears. Implemented as `doc.StripANSI`.

One correctness trap found while testing: `ESC ( B` (select ASCII charset) is *three*
bytes — ESC, an intermediate, then a final byte — so consuming a fixed two leaks the
`B` into the durable output. The general rule is ESC, zero or more intermediates
(0x20–0x2F), then one final byte.

### 8.6 `id` insertion into untidy metadata — ✅ RESOLVED, format spec §9

Insert immediately after the final entry, *before* any whitespace preceding the closing
brace: `{format=csv }` → `{format=csv, id=… }`. Strictly additive — no byte removed —
and it reads tidily, so the whitespace-consuming exception floated earlier is
unnecessary. Covered by `TestInsert` and `FuzzMetaInsertIsAdditive`.

A related detail settled while implementing: `\` may escape only `"` or `\`, and any
other escape is malformed. That keeps escaping a bijection, without which decode and
re-encode cannot agree byte for byte. Recorded in §9.

### 8.7 `VERIFY AGAINST PRIORTOOL` (§8) — ✅ RESOLVED, format spec §8

Verified against priortool's *implementation*, not just its spec, which is
what harvest open question 3 asked for. Four findings:

- **Directory name confirmed.** priortool's own sidecar-directory helper derives
  `<stem>.assets` exactly as §8 says.
- **The payload rule was too thin, and is corrected.** §8 said the `.json` holds "the full
  data payload". priortool persists one JSON per artifact set holding provenance, camera
  state, per-node positions, *and* the result graph verbatim — because data alone would
  lay out differently on re-render (harvest V4). The rendering contract already said "data
  and node coordinates", so §8 alone had drifted from its sibling document. §8 now requires
  whatever offline re-render needs, leaves the shape to the kind, and permits extra keys
  (a priortool-class tool records staleness inputs there).
- **Atomic artifact writes adopted.** priortool writes each file to a temp file and renames
  "so a reader never sees a half-written artifact". notekit used plain `os.WriteFile`, so a
  browser fetching an image mid-run would have received a truncated one. Now
  `doc.WriteFileAtomic`, used for the notebook and for artifacts alike, so the three
  hand-rolled copies that had accumulated cannot drift.
- **The naming divergence is vindicated, not merely intended.** priortool names artifacts
  `<cell-slug>.png` / `<cell-slug>.json`, so a heading rename orphans them — and it
  therefore ships a manual *reattach* screen, described in its own source as "the fix for a
  renamed heading, whose old artifacts no longer match any cell ID". §5's stored `id` is
  what removes the need for that screen. The hazard §5 was written against is not
  hypothetical: it is a shipped feature working around it.

Timing note, retained because it explains why this waited: no sidecar-producing tool exists
even now — the echo and shell executors produce only text — so this never gated M0–M3.
`doc` parses and bookkeeps sidecar references from M0 with the corpus exercising them
synthetically, and the first graph tool is what will exercise them for real.

### 8.9 Slug alignment with priortool — ✅ RESOLVED, format spec §5.2

Checked rather than assumed. priortool's slug algorithm is transcribed into
`doc.TestSlugMatchesPriorTool` as an oracle, and the two agree on every input tried,
including the 60-character truncation and its re-trim. The implementations differ in shape
— priortool writes a separator immediately and trims afterwards, notekit defers it until the
next alphanumeric — so equivalence was worth demonstrating.

One intended difference: priortool requires a non-empty slug, notekit permits an empty one,
because a non-ASCII heading is valid CommonMark and the format must not reject a document
over a naming concern. §8 falls back to `<id>.<ext>`, so nothing depends on it.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Corpus authoring reveals the format needs changes mid-M0 | Expected, not a failure. Spec revision is in scope for M0; the format is draft, not frozen. Freeze only at the M0 gate. |
| Byte-identity regresses quietly as write paths accumulate | Every stage re-runs the whole corpus plus fuzz targets; §7's byte-offset assertions rather than value equality. |
| `serve` grows into an application instead of components | M3 is the forcing function: if clinote v2 can't compose `serve` without editing it, the boundary is wrong. Fold back before any second tool. |
| pty knowledge leaks into kit packages during M3 | Explicit constraint in kit spec §4; review M3's diff for any pty reference outside its `main`. |
| Descoped-but-proven machinery creeps in from priortool | Staleness (R6), crystallisation (V3), and heading hashing (R7's mechanism) are excluded by D4/§5. Reading priortool for the slug rules is the moment this is most likely; slug rules are now cosmetic, so read narrowly. |
| Timestamps and random ids make goldens flaky | Inject both clock and id generator from the start, not after the first flake. |

## 10. Immediate next actions

1. ~~Stage 0 bootstrap (§2)~~ — done: module, tooling, CI, six package skeletons.
2. ~~Resolve §8.1 and §8.2~~ — done: format spec §4 rewritten (§4.1 sections, §4.2
   result position, §4.3 non-cells), plus the image-link pairing rule in §8.
3. ~~M0a `meta`~~ — done: grammar, duplicate rejection, canonical form, append-only
   insertion. 100% coverage, three fuzz targets green, CI running them as smoke.
4. ~~M0b `doc`~~ — done except step 7 (sidecar bookkeeping, which needs the
   filesystem and lands with M0d). Front matter, line scanner, cells, sections, result
   position, slug, id, and all splice operations. 99% coverage, three fuzz targets, and
   a goldmark cross-check over every fixture.
5. ~~M0c — the conformance corpus~~ — done: 11 notebooks, 22 goldens, four walk-driven
   tests. §8.4 and §8.5 resolved along the way.
6. **M0d — `notefmt`** (§3.4) — next, and the last of M0. Parse, list cells, check
   round-trip, report orphans and stale sidecars via `ClassifySidecars`.

Nothing is open. Every question §8 raised is resolved, and the two that needed the priortool
source (§8.7 sidecars, §8.9 slugs) were settled against its implementation rather than its
spec.
