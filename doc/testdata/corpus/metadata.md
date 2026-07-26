---
notekit: 1
title: Metadata
sqlnote-db: ./analysis.db
nested:
  a: 1
  b: [2, 3]
list:
  - one
  - two
---

## Bare and quoted values

```sh {format=csv, label="has spaces", path=/usr/local/bin}
echo hi
```

```output {format=csv, run="2026-07-16T09:41:07Z", tool="clinote/2.0"}
a,b
1,2
```

## Flags and passthrough keys

```sh {verbose, retry=3, custom-key=value, under_score=x}
echo hi
```

```output {truncated}
partial
[notekit: output truncated at 7 bytes]
```

## Escapes in a quoted value

```sh {msg="a \"quoted\" word and a backslash \\"}
echo hi
```

## Hand-authored untidy spacing is never reformatted

```sh {  format = csv ,   verbose  }
echo hi
```
