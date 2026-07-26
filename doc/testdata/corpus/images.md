---
notekit: 1
title: Images
---

## A cell whose section also contains a plain image

```sh
echo hi
```

```output
hi
```

Here is a diagram the author drew, which is prose and must survive a run:

![A hand-drawn diagram](images.assets/hand-drawn.png)

## An image with no provenance comment is never a result

```sh
echo hi
```

![Not a result](pictures/screenshot.png)

## An ordinary HTML comment is not a provenance marker

```sh
echo hi
```

<!-- a note to self -->
![Still not a result](pictures/other.png)

## A provenance comment with no image is prose

```sh
echo hi
```

<!-- notekit:result kind=graph -->

That comment has no image after it, so it is not a result construct.
