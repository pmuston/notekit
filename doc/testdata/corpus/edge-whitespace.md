---
notekit: 1
title: Edge Whitespace
---

## Indented fence

   ```sh
echo indented
   ```

## Tilde fence

~~~sh {format=csv}
echo tilde
~~~

## Heading with closing hashes ##

```sh
echo closing
```

## A hash inside a fence body

```sh
## this is shell, not a heading
echo hi
```

```output
hi
```

## Setext heading below does not begin a section

```sh
echo real
```

Underlined heading
------------------

```sh
this fence is inert: the setext heading did not start a new section
```

## Fence inside a blockquote is not a cell

> ```sh
> echo quoted
> ```

## No blank line between fence and result

```sh
echo tight
```
```output
tight
```
