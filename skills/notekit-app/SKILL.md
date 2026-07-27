---
name: notekit-app
description: >-
  Build a domain-specific notebook application on the notekit kit — a Redis notebook, a
  Postgres notebook, an HTTP notebook. Use whenever the user wants a new notebook tool for
  some engine, says "notebook app for X", "notekit tool for X", "build a <thing> notebook",
  or asks how to add a new engine to notekit. A notebook is one plain CommonMark file; the
  kit supplies the format, the run loop and the browser UI, so the work is an executor plus
  a main. Not for changing notekit itself.
---

# Build a notebook app on notekit

The deliverable is **an executor and a `main`, and nothing else**. The kit already owns the
on-disk format, byte-range splice, run scheduling, capture limits, signal-safe teardown, the
HTMX UI, cell reordering and the notebook picker. If you find yourself wanting to change how
any of that works, stop — that belongs in notekit, and a spec almost certainly settles it.

## 1. Get the kit and read its specs

```bash
go get github.com/pmuston/notekit@latest
```

The specs ship **inside the module**, so nothing needs copying and nothing can drift:

```bash
go list -m -f '{{.Dir}}' github.com/pmuston/notekit
```

Read these from that directory, in order. Each is downstream of the earlier ones.

| File | Why you need it |
|---|---|
| `notekit-harvest.md` | The evidence base. `F`=format, `R`=runtime, `V`=rendering, `P`=posture, `D`=adjudication. When a rule looks arbitrary, its citation is the reason |
| `notekit-format-spec.md` | Normative. §2 front matter and **§2.1** (engine derivation vs the advisory `notekit-tool` key) bear on you most |
| `notekit-rendering-contract.md` | What a result *is* — the kinds, and live vs durable form |
| `notekit-kit-spec.md` | §3.3 `exec`, §3.6 `notetool`, §4 "what a tool binary looks like" |
| `CLAUDE.md` | Its **"Writing an executor"** section is the distilled version of everything that went wrong twice |

All five of notekit's own binaries ship in that directory too, so its source is readable
without cloning anything. Read `cmd/sqlnote/` **in full**. It is the closest exemplar for any
engine you connect to rather than spawn. `cmd/clinote/` is the other shape — a child process
under a pty — and is worth skimming for how it bounds capture and handles cancellation.

## 2. Settle the domain before writing code

Do not start the executor until these are answered. They determine everything downstream, and
guessing wrong means rewriting the executor. Propose answers, then **get them approved**.

1. **Language tag.** The info-string tag on a cell — `redis`, `postgres`. This is what
   identifies the notebook's engine (§2.1); front matter takes no part in it.
   ⚠️ **Check nothing else already claims it.** Two engines sharing one tag is the case §2.1
   says would force a redesign. `sql` is taken by sqlnote — a second SQL tool needs `postgres`
   or similar, and that is a decision to raise, not to make silently.
2. **What is a cell?** One statement, or several? What is the result when there are several?
   **Never parse the engine's syntax to decide** — sqlnote's hard-won rule, and it applies to
   every domain.
3. **The durable result form.** What a reader sees on GitHub. Text, or a `csv`/`jsonl` table?
   If the engine has a canonical CLI, matching its output is usually right — see
   `references/domains.md` for why that solved two otherwise-open problems for Redis.
4. **Front-matter key** for connection config, namespaced: `<tool>-url`, `<tool>-dsn`.
   Per-notebook, arriving via `exec.Notebook.Front` — never a flag on the executor.
5. **Is a self-contained mode possible?** Only if the engine embeds. SQLite does, so a
   notebook with no `sqlnote-db` rebuilds its data from nothing (harvest R2). A network engine
   cannot, and inventing a fake self-contained mode is worse than admitting there is none.
6. **Domain error → `status`.** §7's `status` is a *numeric* code. SQLite has one; Redis has
   string prefixes; Postgres has alphanumeric SQLSTATE. If yours does not fit, omit `status`
   and put the code in the message — and say so, because it may be a real spec question.

`references/domains.md` works these through for Redis and Postgres and lists what varies
between domains. Read it before proposing answers.

## 3. Three rules bind every executor

From notekit's `CLAUDE.md`. Each is easy to get wrong because the obvious thing is the
opposite:

1. **Return raw output.** ANSI stripping and truncation markers are the *format* layer's job.
   An executor that strips destroys the colour the browser renders.
2. **Bound your own capture, and say so.** Set `exec.Result.Truncated` when you drop bytes —
   `run` cannot know otherwise and would treat a short body as complete. `exec.Capture` helps.
3. **Per-notebook config arrives in `exec.Notebook.Front`.** One executor serves many
   notebooks.

Two more that are not negotiable:

- **One session per notebook** (harvest R1), created on open, destroyed on close or signal.
  For anything connection-oriented that means **one dedicated connection held for the
  session's lifetime** — a pool silently loses session state between cells. This is sqlnote's
  `*sql.Conn` lesson and it generalises to every network engine.
- **A domain failure is a *successful* run.** A query that errors persists an `error` block:
  `Done` with `Form` reporting `error`, **not** `Failed`. `Failed` means nothing was persisted.
  The process exits 0.

## 4. What to build

```
<tool>.go       the executor: Executor, Session, Open (reads its front-matter key),
                Lang(), Execute, Close
main.go         flags and wiring only — and a `new` subcommand
main_test.go    end-to-end: run a cell, read the result back out of the file
<tool>_test.go  the executor's own behaviour
Makefile        copy the shape of notekit's
.github/workflows/ci.yml   gofmt, vet, test, race
README.md       what it is, how to run it
docs/<tool>-demo.md        every command in it verified to work
CLAUDE.md       point at the specs in the module cache; record the decisions from step 2
                and *why*; record anything the kit made awkward
```

Use `notetool` for the plumbing — it is public API and needs no `Peers`:

```go
self := notetool.Tool{Name: "rednote", Lang: "redis"}   // no Peers: nothing else to know

self.Create(path, starterCell())   // `new`: front matter + heading + one starter cell
self.Inspect(path)                 // refuse a notebook this binary cannot run
notetool.Resolve(arg, ".")         // the picker
notetool.FindNotebooks(".")        // for -list
```

Leaving `Peers` empty is correct for a tool in its own repo. `Suggest` then reads the
notebook's advisory `notekit-tool` key, which can name tools this build has never heard of.

The `new` subcommand **must write a starter cell** that runs as written: a cell-less notebook
has no tag, and §2.1 derives the engine from tags. Put the subcommand before the flags — Go's
`flag` stops at the first non-flag argument.

## 5. Verify, do not assert

- Run the binary against a real engine. Show the notebook file **before and after** a run.
- Lint what you wrote with notekit's own linter, to prove the file conforms. No clone
  needed:

  ```bash
  go run github.com/pmuston/notekit/cmd/notefmt@latest check yournotebook.md
  ```

  Exit status is 0 clean, 1 problem found, 2 usage or I/O failure, so it works as a commit
  gate in your CI too.
- `gofmt`, `go vet`, `go test`, and **`go test -race`** — the scheduler is concurrent, so
  race is not optional.
- If the tool has a demo guide, every command in it must actually work.

## 6. Report what the kit made awkward

notekit's second consumer proved the `exec` abstraction held but was **incomplete** — only a
new domain could show it, and `exec.Open` had to start receiving front matter as a result. You
are a further domain. If you need something the kit does not expose, that is a finding about
the kit worth reporting, not something to work around silently.
