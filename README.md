# notekit

A notebook is **one plain CommonMark file**. Cells are fenced code blocks; results are
written back into the same file as ordinary Markdown. The file is the artifact — any app is
merely a runner over it.

````markdown
---
notekit: 1
title: Parts inventory
---

## Schema

```sql
CREATE TABLE parts (name TEXT PRIMARY KEY, qty INTEGER NOT NULL);
```

```output {run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
OK
```
````

That renders as a finished article on GitHub with nothing app-specific showing, greps like
text, diffs like text, and **round-trips byte-identically** — a tool rewrites exactly the
byte ranges it executed and copies every other byte through.

notekit is the Go library implementing that format and its runtime mechanics, so a new
notebook tool is an executor plus a `main`.

## Why a library

clinote, graphtool and priortool each independently converged on the same design: one
CommonMark file per notebook, fenced blocks as cells, results persisted into or beside the
file, a single static binary, offline capability, plain-text durable artifacts. This is
that convergence extracted once, third time through.

## The tools

Two are real notebook applications; three are development tools.

| Binary | What it is |
|---|---|
| `clinote` | shell notebook — cells are commands in one persistent shell |
| `sqlnote` | SQL notebook — cells are SQLite queries on one connection |
| `notefmt` | linter and inspector. Never writes to a notebook |
| `noterun` | runs cells with no server, to exercise the async loop alone |
| `noteserve` | the browser UI with a toy executor, to exercise `serve` alone |

`clinote` and `sqlnote` are deliberately different in shape — a pty child with text results
versus a database connection with structured results. That contrast is what proved the
abstraction: neither `doc`, `meta`, `kind`, `run` nor `serve` needed changing to go from
shell to SQL.

## Quickstart

```bash
make bins
./bin/sqlnote examples/parts.md
```

Open the URL it prints, click **Run all**, then look at
[examples/parts.md](examples/parts.md) again — the results are now in the file. Its results
are committed, so it reads as a finished article before anything runs.

[docs/sqlnote-demo.md](docs/sqlnote-demo.md) is a five-minute guided tour; every command in
it is verified to work.

Start one of your own with `new`, which writes front matter, a title from the filename, and
one starter cell of that tool's own language — runnable as written:

```bash
./bin/sqlnote new report.md
./bin/clinote new checks.md
```

It refuses to overwrite an existing file, and the subcommand goes before any flags. Open a
notebook with the wrong tool and it says so before starting, rather than failing once per
cell when you click Run:

```
clinote: report.md has "sql" cells, and clinote runs "sh" cells
  try: sqlnote report.md
```

Lint a notebook without running it:

```bash
./bin/notefmt check examples/parts.md
```

Exit status is 0 clean, 1 problem found, 2 usage or I/O failure — so it works as a commit
gate.

## Layout

```
doc/     document model: parse, cells, slugs, byte-range splice, result writers
meta/    info-string metadata grammar: parse + canonical serialise
run/     run scheduler: async execution, capture limits, splice, atomic save
exec/    executor and session contracts; exec/echoexec is the reference impl
kind/    result-kind registry: durable writers and live renderers
serve/   Echo handlers + HTMX templates + go:embed assets
cmd/     the five binaries
```

## Design commitments

These decisions drive most of the implementation:

**Byte-range splice is the only write path.** Every construct records its exact byte span,
and there is deliberately no "serialise the whole tree" API. That absence is what
guarantees round-trip identity. Prose is never reflowed, whitespace never normalised,
metadata the tool did not write never reordered.

**The format assigns almost no semantics.** Only `notekit` and `title` in front matter, plus
the keys the format spec reserves, mean anything. Everything else — including *all* keys on
source fences — is preserved byte-for-byte and handed to the runtime uninterpreted.

**A notebook's engine comes from its cells; front matter only offers a hint.** A file of
`sql` cells is a SQL notebook because its cells say `sql` — that decision is never taken
from front matter, so there is no second source of truth about what runs. The optional
`notekit-tool` key says which application to *try instead*, which cell tags cannot: notebook
tools live in separate repositories, so no compiled-in list can name them all. It is
advisory — a wrong value earns a warning, never a refusal. And it is why `new` writes a
starter cell rather than relying on the key: a notebook with no cells is the one case
derivation cannot answer.

**Parsing is conservative; tools are loud.** A construct that fails to match the cell rules
is prose, not an error. Errors come only from tools, and only about blocks they were asked
to run or write. Nothing is ever silently repaired.

Results are volatile: a run replaces the whole of a cell's result region. There is no
freeze, no staleness tracking, no protection.

Cells can be reordered with the ↑/↓ buttons. The unit is the whole section, so a cell's
prose and results travel with it, and identity is unaffected — `id` is stored rather than
derived from position, so sidecar artifacts stay attached. Reordering does change what a
run-all does, since document order *is* execution order.

## Specs

The specs are the authority for everything built here, and the code is downstream of them.
Read the first four in order — each is downstream of the earlier ones:

| File | Owns |
|---|---|
| [notekit-harvest.md](notekit-harvest.md) | evidence base: what the three prior tools proved, and the adjudications |
| [notekit-format-spec.md](notekit-format-spec.md) | normative on-disk format: cells, results, sidecars, identity, round-trip policy |
| [notekit-rendering-contract.md](notekit-rendering-contract.md) | what a result *is*: core kinds, live vs durable forms, extension rules |
| [notekit-kit-spec.md](notekit-kit-spec.md) | the Go library: package layout, responsibilities, style constraints |
| [notekit-implementation-plan.md](notekit-implementation-plan.md) | non-normative build plan, staged M0–M3 with gates |

If the plan and the specs disagree, the specs win.

## Testing

```bash
make test          # go test ./...
make race          # the scheduler is concurrent, so this is not optional
make fuzz          # six targets, 30s each; FUZZTIME=2m for longer
make lint          # go vet + gofmt check
make check-corpus  # lint the acceptance corpus with notefmt itself
```

The acceptance suite is a **format conformance corpus** in `doc/testdata/corpus/`, each
notebook paired with a `.cells` golden (parsed structure) and a `.spliced` golden (the
document after running every cell). Dropping in a `.md` file adds it to four walk-driven
tests:

```bash
go test ./doc -run TestCorpus -update   # regenerate goldens, then read the diff
```

A golden that changed silently is a spec change nobody reviewed.

Two things carry more weight than usual. **Fuzzing is load-bearing** — byte-identity and
append-only insertion are exactly the properties a fuzzer can falsify, and one already has:
an unclosed source fence used to corrupt the cell it belonged to. And **goldmark is kept as
an independent oracle**: `doc` scans lines itself, and `doc/scan_goldmark_test.go`
cross-checks that scanner against goldmark on every fixture.

## Dependencies

Go stdlib first. goldmark (test oracle), Echo, `modernc.org/sqlite` (pure Go, no CGO),
`creack/pty`, and HTMX vendored under `serve/assets/`. All web assets are `go:embed`-ed:
no CDN, no network to render, no frontend build step. One static binary per tool, with no
runtime file dependencies beyond the notebook and its sidecar directory.

## Status

Go 1.25.4. All four harvest validation gates are met: the harvest is adjudicated, the
format spec has a golden-file corpus and no remaining placeholders, clinote v2 runs on the
kit at functional v1 parity, and sqlnote exists as a second consumer in a non-shell domain.

Each spec ends with a **FUTURE** section. Those are recorded so the contracts do not
preclude them — not a roadmap, and not implemented.
