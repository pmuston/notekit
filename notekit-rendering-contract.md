# notekit Rendering Contract — v1

> Defines what a result *is*: the closed set of core result kinds, their live and
> durable forms, and the rule by which a domain tool admits a new kind. Companion
> to the format spec (which owns all on-disk syntax) and the kit spec (whose `kind`
> package implements the registry).

Status: draft v1.

---

## 1. The two forms rule

Every result kind must define exactly two representations:

1. **Live form** — what the browser renders during a session: payload shape and
   renderer behaviour.
2. **Durable form** — what persists, in plain CommonMark per the format spec:
   either an inline result block (`output`/`error` fence) or a sidecar artifact
   plus reference (format spec §8).

A kind without a defined durable form is inadmissible. The durable form is the
artifact of record; the live form is a courtesy of the running tool. Anything that
exists only live (colour, sortability, interactivity) must degrade to nothing —
the durable form must stand alone on GitHub.

## 2. Core kinds (closed set)

Fixed for v1. Tools must support all three.

### 2.1 `text`

- Durable: `output` fence, `format` absent or `text`. Body is captured text, ANSI
  stripped, one trailing newline.
- Live: preformatted block; ANSI colour rendered live only (harvest F12).

### 2.2 `table` (durable as `csv` or `jsonl`)

- Durable: `output` fence with `format=csv` (RFC 4180, header row required — priortool
  convention) or `format=jsonl` (one JSON object per line).
- Live: sortable HTML table; JSONL flattened to columns by first-seen key order.
  Cells beyond the runtime output cap follow the standard truncation rule; the
  table renders what was persisted, never a fuller live-only version.
- Executors declare which serialisation they emit; the kit does not transcode
  between `csv` and `jsonl`.

### 2.3 `error`

- Durable: `error` fence per format spec §7 — first-class, never folded into
  `output`. Body is the domain's error text as captured; `status` metadata carries
  the numeric code where the domain has one.
- Live: visually distinct from output (the classic red block); rendered from the
  same durable body — no richer live-only error detail.

## 3. Extension kinds

A domain tool may register additional kinds. Admissibility rule (harvest §5.3):
a kind is admissible iff it defines (a) its live payload and renderer and (b) its
durable CommonMark form, which must be one of:

- **Typed inline fence**: an `output` fence with a domain `format` value whose body
  is a plain-text serialisation (must remain grep-able and diff-sane); or
- **Sidecar + reference**: rendered artifact and full data payload in the sidecar
  directory, standard image/link reference with provenance comment in the notebook
  (format spec §8). Required for anything binary or visual.

Registration is compile-time in the tool binary (`kind` registry, kit spec §3.3/
§3.5): a kind name (the `format`/`kind` metadata value), a live renderer, and a
durable writer. Kind names are lower-case, domain-prefixed for non-core kinds
(`graph`, or `cy-plate` style where collision is plausible).

### 3.1 Worked example: `graph`

The kind the harvest anticipates for Cypher-family tools (proven live stack:
Sigma.js/graphology, harvest V2):

- Live: interactive Sigma canvas; seeded deterministic layout (harvest V4); node
  drag permitted during the session.
- Durable: sidecar `<slug>--<id>.png` (rendered figure) + `<slug>--<id>.json` (full graph data
  and node coordinates, sufficient for offline re-render with no live database) +
  provenance comment and image link in the notebook.
- Volatile like everything else: re-running the cell overwrites both sidecar files
  (harvest D4).

## 4. Lifecycle

One lifecycle in v1: **volatile**. Every run replaces the cell's result blocks and
overwrites its sidecar files. There is no freeze, no staleness, no protection.

Crystallisation — compose-then-freeze with staleness tracking, as built in priortool —
is explicitly *not* a core lifecycle (harvest D4). It is the exception, and remains
domain machinery inside priortool-class tools. If a second crystallising tool ever
appears, lifecycle keys get standardised then (format spec FUTURE), not before.

## 5. Conformance

A tool conforms to this contract if:

1. It renders and persists all three core kinds per §2.
2. Every extension kind it registers satisfies the two-forms rule of §3.
3. Its durable output for any kind passes the format conformance corpus (round
   trip, fence safety, truncation, canonical metadata).
4. Nothing renders live that misrepresents the durable form — the live view may be
   prettier, never *fuller*.

## FUTURE (recorded, do not implement)

- `relation` as a first-class kind with a typed column contract — the prerequisite
  for the polyglot/SQLite-join notebook and cross-cell named results.
- Crystallised lifecycle standardisation (second tool trigger).
- Diagram/figure kinds for the wider fig-tool family (gfig-rendered durable SVG is
  a natural fit for the typed-inline path).
