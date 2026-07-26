//go:build !unix

// Command clinote is a shell notebook. It requires a Unix-like system: the persistent
// shell runs under a pty, which is what lets state carry between cells and lets programs
// detect a terminal and emit colour.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "clinote: requires a Unix-like system (it runs a shell under a pty)")
	os.Exit(2)
}
