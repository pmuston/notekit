---
notekit: 1
title: Identity
---

## Results

```sh
echo one
```

## Results

Two cells share the slug `results`. That is not a collision and neither gets a
positional suffix.

```sh
echo two
```

## 日本語

A non-ASCII heading yields an empty slug, which is permitted, not an error.

```sh
echo unicode
```

## A heading long enough that its slug is truncated at exactly sixty characters yes

```sh
echo long
```

## Cell with an identity but an inline result

An id may be present even when the result is inline; it is simply never assigned
that way by a tool.

```sh {id=aaaa2345}
echo has-id
```

```output
has-id
```

## Cell with an identity and a sidecar

```cypher {format=graph, id=zzzz2345}
MATCH (n) RETURN n
```

<!-- notekit:result kind=graph, run="2026-07-20T08:00:00Z" -->
![Cell with an identity and a sidecar](identity.assets/cell-with-an-identity-and-a-sidecar--zzzz2345.png)
