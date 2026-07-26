---
notekit: 1
title: Unclosed
---

## A closed cell before the unclosed one

```sh
echo fine
```

```output
fine
```

## The source fence below is never closed

Its cell is readable and runnable, but it has no result position, so a tool must
refuse to persist a result rather than append into the fence body.

```sh
echo unterminated
this line is inside the fence
## so is this
