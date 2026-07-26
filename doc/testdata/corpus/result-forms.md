---
notekit: 1
title: Result Forms
---

## Output form

```sh
echo hi
```

```output
hi
```

## Error form

```sh
false
```

```error {status=1}
```

## Sidecar form

```cypher {id=k3m7q2vf}
MATCH (m:Module)-[r]->(n) RETURN m, r, n
```

<!-- notekit:result kind=graph, run="2026-07-16T10:02:55Z", tool="graphtool/2.0" -->
![Sidecar form](result-forms.assets/sidecar-form--k3m7q2vf.png)

## Two output blocks are read as one cell's results

```sh
echo twice
```

```output
first
```

```output
second
```

## Mixed forms are read together

```sh {id=bbbb2345}
echo mixed
```

```output
text part
```

<!-- notekit:result kind=graph -->
![Mixed forms](result-forms.assets/mixed-forms--bbbb2345.png)

## Prose terminates the result position

```sh
echo bounded
```

```output
inside
```

This paragraph is outside the result position and survives any run.

```output
so this block is prose, not a result
```
