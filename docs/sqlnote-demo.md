# sqlnote in five minutes

A SQL notebook is one plain Markdown file. Cells are `sql` fenced blocks; results are
written back into the same file as plain CommonMark, so the notebook renders on GitHub
with nothing app-specific showing.

## Build

```bash
make bins
```

Five static binaries land in `bin/`. No CGO, no runtime file dependencies beyond the
notebook itself.

## Run the shipped example

```bash
./bin/sqlnote examples/parts.md
```

It prints a URL and which database it is using:

```
sqlnote: http://127.0.0.1:8080  (examples/parts.md, in memory, self-contained)
```

Open the URL. Click **Run all**, or run cells one at a time. Then look at
[examples/parts.md](../examples/parts.md) again — the results are now *in the file*.

Its results are committed, so you can read the finished article before running anything.
Re-running replaces them: results are volatile, and a second run of the same query
produces a byte-identical file apart from its timestamp.

## The two data modes

The example names no database, so it runs **in memory** and is *self-contained*: its cells
build the data and a run from empty reconstructs it. Nothing is written beside the
notebook.

To **bind** it to a file instead, add one line to the front matter:

```yaml
---
notekit: 1
title: Parts inventory
sqlnote-db: ./parts.db
---
```

Now the data outlives the process, and the path resolves against the notebook — so it
means the same wherever you launched from. That key is ordinary front matter: the format
reserves only `notekit` and `title` and hands everything else to the tool untouched.

## What the cells do

| Cell shape | Result |
|---|---|
| ends in a `SELECT` | a table — sortable in the browser, csv in the file |
| ends in `CREATE`, `INSERT`, `PRAGMA`… | `OK`, with a row count when rows changed |
| the engine rejects it | an `error` block carrying SQLite's own result code |

Several statements in one cell are fine; the last one decides which of those you get.

`csv` is the default because it stays readable on GitHub. Ask for `jsonl` when `NULL` has
to stay distinct from an empty string, which csv cannot express:

````markdown
```sql {format=jsonl}
SELECT name, qty, NULL AS reorder_ref FROM parts WHERE qty < 50;
```
````

## One connection, so state carries

Every cell in a notebook shares one database connection for the life of the process.
Temp tables and `PRAGMA`s set in one cell are still there in the next — which is why the
example can build a temp table in one cell and read it in another.

That also means a self-contained notebook loses everything when you stop the server, by
design. Bind it to a file if you want otherwise.

## Start your own

```bash
./bin/sqlnote new scratch.md
```

That writes the file and opens it:

````markdown
---
notekit: 1
title: Scratch
notekit-tool: sqlnote
---

## First query

```sql
SELECT 'hello from sqlnote' AS greeting;
```
````

Four things about it worth knowing:

- **The starter cell runs as written.** Click Run and you get a table back, so the notebook
  is working before you have typed anything.
- **The title comes from the filename.** `parts-list.md` becomes "Parts list";
  `my_notes.md` becomes "My notes".
- **It never overwrites.** Point `new` at a file that exists and it refuses — the file is
  the artifact, and a mistyped path should not cost you work.
- **The subcommand goes first**, before any flags: `sqlnote new -addr :9000 scratch.md`. Put
  it after and sqlnote tells you so rather than printing usage at you.

Nothing else is invented — no prose, no placeholder result, and no `sqlnote-db`, since a
notebook is self-contained until you bind it.

### By hand, if you prefer

Any Markdown file with `notekit: 1` front matter and a heading above a `sql` fence is a
notebook:

````markdown
---
notekit: 1
title: Scratch
---

## Some numbers

```sql {format=csv}
SELECT 1 AS n UNION ALL SELECT 2;
```
````

Then `./bin/sqlnote scratch.md`. With no argument sqlnote picks the notebook in the current
directory; with several it names them rather than guessing.

The **Add a cell** form at the foot of the page writes further cells for you. Prose and cell
sources are editable in place: click any paragraph or any source block. An unsaved edit is
flagged, and nothing but the region you edited is rewritten.

### Opening a notebook in the wrong tool

```bash
./bin/sqlnote a-shell-notebook.md
```

```
sqlnote: a-shell-notebook.md has "sh" cells, and sqlnote runs "sql" cells
  try: clinote a-shell-notebook.md
```

It refuses before starting, rather than letting you click Run and fail once per cell.

What a notebook can run is worked out from the tags its cells carry — never from front
matter. The `notekit-tool: sqlnote` line `new` writes is a hint for *other* tools, and its
real value is pointing at applications neither of these binaries has heard of. It carries no
authority: edit it to something wrong and you get a warning, not a refusal, and
`notefmt check` will tell you it disagrees with the cells.

## Check a notebook without running it

```bash
./bin/notefmt check examples/parts.md
./bin/notefmt list examples/parts.md
```

`check` verifies the file round-trips byte-identically and reports anything odd — a
malformed info string, an unclosed fence, a stale or orphaned artifact. It never writes to
a notebook. Exit status is 0 clean, 1 problem found, 2 usage or I/O failure, so it works as
a commit gate.

## The other binaries

| Binary | What it is |
|---|---|
| `sqlnote` | SQL notebook — this guide |
| `clinote` | shell notebook: cells are shell commands in one persistent shell |
| `notefmt` | linter and inspector; never writes |
| `noterun` | runs cells with no server, to see the async loop on its own |
| `noteserve` | the browser UI with a toy executor, for looking at `serve` alone |

`clinote` is the same shape with a different engine, `new` included:

```bash
./bin/clinote                  # picks the notebook in the current directory
./bin/clinote new checks.md    # a shell notebook with a runnable starter cell
```
