# Domain analyses

Worked answers to SKILL.md step 2 for two engines, and the axes that vary. Read the one
nearest your engine, then propose answers for yours.

## What varies between domains

| | sqlnote (SQLite) | rednote (Redis) | pgnote (Postgres) |
|---|---|---|---|
| language tag | `sql` | `redis` | **`sql` collides** — see below |
| front-matter key | `sqlnote-db` | `rednote-url` | `pgnote-dsn` |
| self-contained mode? | **yes** — SQLite embeds | no — network service | no |
| session-scoped state | temp tables, PRAGMAs | `SELECT n`, `MULTI`, `WATCH` | temp tables, `search_path`, prepared statements |
| durable result form | `csv` table | redis-cli reply text | `csv` table |
| error → `status` | numeric result code | string prefix, no number | **SQLSTATE, alphanumeric** |
| blocking commands | none | `BLPOP`, `SUBSCRIBE` | `LISTEN`, long queries |
| destructive one-liners | `DROP TABLE` | `FLUSHALL` | `DROP DATABASE`, `TRUNCATE` |

Everything in the first column is settled and shipped. The bold cells are live design
questions — raise them, do not decide them alone.

## Redis — a worked example

### No self-contained mode, and do not fake one

sqlnote answers harvest R2 (self-contained versus bound data) because SQLite *embeds*: with no
`sqlnote-db`, the notebook runs in memory and its own cells rebuild the data from nothing.
Redis is a network service, so a Redis notebook is **always bound** to a server.

- `rednote-url` absent should mean a sensible default (`redis://127.0.0.1:6379/0`), not
  "in memory".
- **Say which server you are talking to on the startup line**, as sqlnote says "in memory,
  self-contained". A user must not have to guess which Redis they are about to write to.
- A committed notebook records commands, not data. Put that in the demo guide rather than
  letting someone discover it.

### The durable form: match `redis-cli`, and get two problems solved free

Render replies the way `redis-cli` renders them. This is not only familiarity — its formatter
is a total function from arbitrary bytes to printable ASCII, which answers the two things that
otherwise have no good answer in a plain-text file:

- **nil is distinguishable from empty.** `(nil)` versus `""`. csv cannot express that
  difference, which is why the format offers `jsonl` at all.
- **Binary-safe values survive.** Redis strings are arbitrary bytes; a value with NUL or
  invalid UTF-8 has no faithful plain-text form. redis-cli escapes it as `\xNN`, so the
  notebook stays valid CommonMark and the value stays exact.

| Reply | Rendered as |
|---|---|
| simple string | `OK` — bare |
| error | `(error) ERR value is not an integer or out of range` |
| integer | `(integer) 42` |
| bulk string | `"value"` — quoted, with `\xNN` and the usual escapes |
| nil | `(nil)` |
| empty array | `(empty array)` |
| array | `1) "a"` — 1-based, index padded, nested indented |
| double / boolean (RESP3) | `(double) 3.14` / `(true)` |
| map / set (RESP3) | `1# "f" => "v"` / `1~ "m"` |

**Verify these against a real `redis-cli` rather than trusting the table** — they vary by
version (`(empty array)` was once `(empty list or set)`), and the bytes get committed to git.
A golden test against real output beats a plausible implementation.

### Do NOT reproduce the prompt or the command echo

`redis-cli` interactively prints `127.0.0.1:6379> SET k v` before each reply. **Never write
that into a result block.** The command is already in the source fence directly above, so
echoing it duplicates the notebook's own content and makes the file misrepresent what it
captured.

This is clinote's worst bug in a new costume: prompt and echoed input leaking into captured
output, which needed a structural fix (stdin a pipe, stdout the pty) to remove. Three commands
produce three replies, matched positionally against the fence above, and nothing else.

### Remaining decisions for Redis

- **A failure part-way through a cell.** Redis pipelines carry on, and the format requires a
  run to write exactly one construct — so a cell where anything errored should persist an
  `error` fence holding the whole transcript with `(error) …` inline where it happened. Confirm
  that reads well before committing to it.
- **Argument quoting.** Values contain spaces; redis-cli splits shell-like. Match it, and test
  values with embedded quotes and escapes — the quoting you emit must survive being read back.
- **Blocking commands.** `BLPOP`/`WAIT` must honour context cancellation so the kit's cancel
  route works. `SUBSCRIBE` never returns and should be refused with a clear message rather
  than wedging the session.
- **`FLUSHALL` is one keystroke from a notebook**, with no undo, and the notebook is in git
  while the data is not. Decide: run, warn, or require a flag — and write the decision down.

## Postgres — the two questions it forces

### The tag collision

Postgres wants `sql`, which sqlnote already claims. Format spec §2.1 names this as the case
that would force a rethink:

> if two engines ever share one language tag — two different SQL backends, say — the tag stops
> distinguishing them and derivation becomes genuinely insufficient

`notekit-tool` cannot rescue it, being advisory by construction. The plausible fix is that a
tag names the **dialect** not the family — `sqlite`, `postgres`, `mysql` — which means
sqlnote's claim on `sql` was too broad. But narrowing it breaks every existing notebook,
including notekit's own `examples/parts.md` and its conformance corpus.

**Raise this before writing a second SQL tool.** Using `postgres` as the tag sidesteps it for
one tool, and that is probably right, but it is the user's decision.

### SQLSTATE does not fit `status`

§7 defines `status` as "the domain's numeric error code". Postgres codes are five characters
and not always digits — `23505`, but also `42P01`. So either omit `status` and carry the code
in the message, or raise it as a spec question. Prefer omitting plus raising: a fourth domain
finding a gap in §7 is exactly the kind of evidence the format spec is built from.

### Session-scoped state to respect

`search_path`, `SET LOCAL`, temp tables, prepared statements and open transactions all live on
the connection. Hold one `*sql.Conn` for the session's lifetime — the same rule as sqlnote,
for the same reason.

## A note on choosing an engine driver

notekit's dependency policy is stdlib first, and anything else needs a justification comment at
the import site. A well-known driver is a reasonable choice; justify it there. Consider whether
a test double exists (`miniredis` for Redis, a container or `pgx` test harness for Postgres) so
the suite runs without a live server — and if you skip that, tests must skip cleanly rather
than fail, which means CI proves nothing about the executor. Say so if you take that route.
