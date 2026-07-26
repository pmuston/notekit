---
notekit: 1
title: Fence Safety
---

## Body containing a three-backtick run

```sh
echo nested
```

````output
```
three backticks inside
```
````

## Body containing a four-backtick run

```sh
echo nested
```

`````output
````
four backticks inside
````
`````

## Body containing a five-backtick run

```sh
echo nested
```

``````output
`````
five backticks inside
`````
``````

## Source fence itself uses four backticks

````sh {format=csv}
echo '```'
````

```output
```
