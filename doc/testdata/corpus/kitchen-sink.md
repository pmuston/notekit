---
notekit: 1
title: Kitchen Sink
clinote-session: shell
---

Opening prose that no tool ever touches.

## Disk usage by top-level directory

Some explanation before the cell.

```sh {format=csv}
du -d1 -h | sort -hr | head -3
```

```output {format=csv, run="2026-07-16T09:41:07Z", tool="clinote/2.0"}
size,path
1.2G,./data
480M,./vendor
```

Prose between cells.

## A command that fails

```sh
dv
```

```error {status=127, run="2026-07-16T09:44:12Z", tool="clinote/2.0"}
zsh: command not found: dv
```

## Not runnable, just an example

This section's only fence is untagged, so it holds no cell.

```
just an illustration
```

## Two fences, one cell

```sh
echo real
```

The fence below is an inert example: it is not the first in its section.

```sh
echo example
```
