---
notekit: 1
title: Sections
---

## Outer

```sh
echo outer
```

```output
outer
```

### Inner

Sections end at the next heading of any level, so this is its own cell.

```sh
echo inner
```

```output
inner
```

#### Deeper still

```cypher
MATCH (n) RETURN n
```

## Back to level two

```sh
echo back
```

### Heading between a fence and an output block

```sh
echo orphaned
```

### The output below belongs to no cell

```output
this fence is first in its section, so this section has no cell
```
