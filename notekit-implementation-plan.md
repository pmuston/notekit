# notekit Implementation Plan — v1

> How the kit gets built. Operationalises the kit spec's M0–M3 (`notekit-kit-spec.md`
> §6) into ordered stages with acceptance gates, names the sequencing constraints the
> milestone list glosses over, and lists the spec questions each stage will force.
> Not normative: the three specs govern. This document is disposable once M3 lands.

Status: draft · No code exists yet; Stage 0 is the next action.

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

**Gate:** table-driven tests over valid and invalid info strings; parse → serialise
identity for every well-formed input; `FuzzMetaRoundTrip` green (§7).

### 3.2 M0b — `doc` (format spec §2–§8)

Depends on `meta`. Build in this internal order, because each step's tests need the
previous:

1. **Front matter.** Split on `---` delimiters. Extract only `notekit` (must be `1`)
   and `title`; treat the remainder as an **opaque byte range**, not parsed YAML.
   Refuse any file lacking front matter or `notekit: 1`. *Deliberately not*
   round-tripping YAML through a marshaller — that is the simplest way to guarantee
   §2's byte-for-byte passthrough, and it means no YAML dependency beyond reading two
   scalars.
2. **Structural scan.** goldmark as scanner only (harvest P2). Every construct records
   its exact byte span. Nothing in `doc` serialises a tree.
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
7. **Sidecar bookkeeping** per §8.1: map `id` → sidecar files; detect renames (slug
   changed, `id` matched) and orphans (`id` matches no cell). Report only — never
   delete.
8. **Splice operations**, the only write path: replace a cell's result blocks, append
   an `id` to a source fence, edit a prose range, append a cell. Include the prose-range
   splice now even though nothing exercises it until M2 — it is the same machinery.

### 3.3 M0c — the conformance corpus

Authored alongside M0b, living with `doc`. Cover all nine §11 items. The cases that
matter most, because they encode decisions rather than mechanics:

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

### 3.4 M0d — `notefmt`

CLI: parse a notebook, list cells (heading, slug, `id`, language, result kind), check
round-trip identity, report orphans. Exit non-zero on any format error. Useful
permanently as a linter and as the debugging tool for every later stage.

**M0 gate:** full corpus green; `notefmt` round-trip-checks every corpus file and every
spec document's embedded examples; both fuzz targets green. No execution code exists.

## 4. M1 — `exec` + `run` + `kind` (durable half)

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

**Gate:** CLI demo runs cells through the echo executor; corpus still green; error
blocks, truncation, and cancellation all covered; teardown verified under both signals.

## 5. M2 — `serve`

Echo handlers plus HTMX templates, all assets `go:embed`-ed, no CDN, no frontend build
step.

- Notebook view; cell run (POST → run ID); status polling at 500 ms with spinner;
  result rendering through the `kind` live registry.
- Live-only niceties that must not reach disk: ANSI colour in the browser, sortable
  tables for `csv`/`jsonl` with JSONL flattened to columns by first-seen key order.
- Inline prose editing with an unsaved-changes indicator, and save — exercising the
  prose-range splice built in M0b.
- Components, not an application: each tool composes its own `main` around them.

**Gate:** full HTMX loop against the echo executor. Verify the rendering contract's §5.4
rule explicitly — the live view may be prettier, never *fuller* than what persisted.

## 6. M3 — clinote v2

First real consumer, and the test of whether the kit's boundaries are right.

- `main` owns: flags (`clinote notebook.md`, picker when no arg), pty shell executor,
  sentinel exit-code capture, registration, `serve` composition. **All pty knowledge
  lives here and in no kit package.**
- Parity with clinote v1 is **functional, not file-level** (harvest D8) — v1 files need
  not open.
- Fold any kit changes M3 forces back into the kit *before* a second tool starts
  (validation gate 4).

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

### 8.3 Sidecars stranded by a kind change — genuine gap, resolve before M2

A cell that produced a sidecar (and so carries an `id`) is edited to produce text
instead. Volatile lifecycle overwrites *result blocks*, and the rendering contract says
re-running overwrites sidecar files — but nothing overwrites a sidecar that the new run
doesn't produce. The old files linger, and they are **not** orphans by §8.1's
definition, because their `id` still matches a live cell. Options: treat "sidecar files
under a live `id` not written by the latest run" as a reportable stale state, or have
`run` delete them as part of volatile replacement. The second contradicts "never delete
silently"; the first is probably right and needs §8.1 wording.

### 8.4 Truncation and fence length ordering — pin in the corpus, M0c

The truncation marker is appended to the body (§6) and fence length derives from the
longest backtick run in the body. If truncation cuts mid-backtick-run the two rules
interact. Fix the order — truncate, append marker, *then* compute fence length — and
pin it with a corpus case.

### 8.5 ANSI stripping scope — resolve before M1

"ANSI escapes stripped" (§6) needs a definition: CSI SGR sequences only, or all
escape sequences including cursor movement, OSC, and control characters? A shell
executor will emit more than colour. Recommend stripping all CSI and OSC sequences and
normalising remaining control characters except `\t` and `\n`.

### 8.6 `id` insertion into untidy metadata — pin in the corpus, M0c

Append-only insertion into `{format=csv }` (trailing space inside the braces) yields
`{format=csv , id=…}`. Acceptable but ugly. Decide whether insertion may consume
whitespace immediately before the closing brace — a narrow, well-defined exception to
"changes nothing else" — and pin it.

### 8.7 `VERIFY AGAINST PRIORTOOL` (§8) — not on the critical path

Sidecar directory name and payload JSON shape are still unverified against priortool.
Note the timing: **no sidecar-producing tool exists until after M3** — the echo and
shell executors produce only text. So `doc` must parse and bookkeep sidecar references
from M0 (the corpus exercises them synthetically), but the priortool verification only
gates the first graph tool. Do not let that defer the `id` mechanism, which the corpus
depends on from M0 onward.

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
3. **M0a `meta`** (§3.1) — next. Depends on nothing.
4. M0b `doc` (§3.2), unblocked now that the cell model is settled.

Still open before the stages that need them: §8.3 (stranded sidecars, before M2), §8.4
and §8.6 (corpus-pinned, M0c), §8.5 (ANSI scope, before M1), §8.7 (priortool verification,
after M3).
