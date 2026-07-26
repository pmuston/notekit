---
notekit: 1
title: Parts inventory
notekit-tool: sqlnote
---

A **self-contained** SQL notebook: it names no database, so it runs in memory and its
cells rebuild the data from nothing every time. Running every cell in order is the whole
story — there is no hidden state to restore.

To bind it to a file instead, add `sqlnote-db: ./parts.db` to the front matter above.

## Schema

One connection serves every cell, so anything created here is visible later.

```sql
CREATE TABLE parts (
  name      TEXT PRIMARY KEY,
  qty       INTEGER NOT NULL,
  unit_cost REAL    NOT NULL
);
```

```output {run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
OK
```

## Load

Several statements in one cell are fine.

```sql
INSERT INTO parts (name, qty, unit_cost) VALUES
  ('bracket', 120, 0.40),
  ('washer',  340, 0.05),
  ('flange',   45, 3.20),
  ('gasket',    8, 1.75);
```

```output {run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
OK, 4 rows affected
```

## Stock value by part

A cell whose last statement returns rows becomes a table. `csv` is the default, and
renders as a sortable table in the browser while staying readable here on GitHub.

```sql {format=csv}
SELECT name, qty, unit_cost, qty * unit_cost AS value
FROM parts
ORDER BY value DESC;
```

```output {format=csv, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
name,qty,unit_cost,value
flange,45,3.2,144
bracket,120,0.4,48
washer,340,0.05,17
gasket,8,1.75,14
```

## Totals

```sql {format=csv}
SELECT count(*) AS parts, sum(qty) AS units, round(sum(qty * unit_cost), 2) AS value
FROM parts;
```

```output {format=csv, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
parts,units,value
4,513,223
```

## Low stock, as jsonl

`jsonl` keeps `NULL` distinct from an empty string, which csv cannot express.

```sql {format=jsonl}
SELECT name, qty, NULL AS reorder_ref
FROM parts
WHERE qty < 50
ORDER BY qty;
```

```output {format=jsonl, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
{"name":"gasket","qty":8,"reorder_ref":null}
{"name":"flange","qty":45,"reorder_ref":null}
```

## A temp table, proving state carries

Temp tables live on the connection. That this one survives to the next cell is what
"one session per notebook" means for a database.

```sql
CREATE TEMP TABLE bulk AS
SELECT name, qty FROM parts WHERE qty >= 100;
```

```output {run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
OK
```

## Reading it back

```sql {format=csv}
SELECT * FROM bulk ORDER BY qty DESC;
```

```output {format=csv, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
name,qty
washer,340
bracket,120
```

## When SQL goes wrong

A failed statement is a *successful run* that records the engine's own error and code —
never output pretending to be a result.

```sql
SELECT * FROM no_such_table;
```

```error {status=1, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
SQL logic error: no such table: no_such_table (1)
```

## A constraint violation

SQLite's extended result code comes through as the `status`.

```sql
INSERT INTO parts (name, qty, unit_cost) VALUES ('bracket', 1, 1.00);
```

```error {status=1555, run="2026-07-26T07:33:31Z", tool="sqlnote/1.0"}
constraint failed: UNIQUE constraint failed: parts.name (1555)
```
