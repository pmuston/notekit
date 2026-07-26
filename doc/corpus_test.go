package doc

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// update regenerates golden files instead of comparing against them:
//
//	go test ./doc -run TestCorpus -update
//
// Review the resulting diff before committing — a golden that changed silently is a
// spec change nobody reviewed.
var update = flag.Bool("update", false, "regenerate corpus golden files")

const corpusDir = "testdata/corpus"

// corpusFiles returns every notebook in the corpus, sorted.
func corpusFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(corpusDir, "*.md"))
	if err != nil {
		t.Fatalf("globbing corpus: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no corpus files found in %s", corpusDir)
	}
	sort.Strings(matches)
	return matches
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// checkGolden compares got against the golden file at path, or rewrites it under
// -update.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run with -update to create it)", path, err)
	}
	if got != string(want) {
		t.Errorf("%s is out of date.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// TestCorpusRoundTrip is §11.1: parse then ask for the bytes back, over every corpus
// file, byte-compared. The directory walk means adding a file adds a test.
func TestCorpusRoundTrip(t *testing.T) {
	for _, path := range corpusFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readFile(t, path)
			n, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := n.Bytes(); string(got) != string(src) {
				t.Errorf("round trip changed bytes")
			}
			// Applying no edits must also be byte-identical.
			out, err := n.Apply()
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(out) != string(src) {
				t.Errorf("Apply with no edits changed bytes")
			}
		})
	}
}

// TestCorpusCells covers §11.2, §11.3, §11.7 and §11.8 declaratively: the structure
// the parser sees for each corpus file is written out and compared to a golden, so a
// change in cell detection, result pairing, slug derivation, or id reading shows up as
// a reviewable diff rather than as a silently different behaviour.
func TestCorpusCells(t *testing.T) {
	for _, path := range corpusFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readFile(t, path)
			n, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			checkGolden(t, strings.TrimSuffix(path, ".md")+".cells", describeNotebook(n))
		})
	}
}

// describeNotebook renders a notebook's parsed structure as stable text.
func describeNotebook(n *Notebook) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version: %d\n", n.Version())
	fmt.Fprintf(&b, "title: %q\n", n.Title())
	fmt.Fprintf(&b, "front matter: %d bytes\n", n.FrontMatter().Len())
	fmt.Fprintf(&b, "cells: %d\n", len(n.Cells()))

	for i, c := range n.Cells() {
		fmt.Fprintf(&b, "\n[%d] %s\n", i, c.HeadingText)
		fmt.Fprintf(&b, "    level:     %d\n", c.Level)
		fmt.Fprintf(&b, "    lang:      %s\n", c.Lang)
		fmt.Fprintf(&b, "    slug:      %q\n", c.Slug)
		fmt.Fprintf(&b, "    id:        %q\n", c.ID)
		fmt.Fprintf(&b, "    closed:    %t\n", c.Closed)
		fmt.Fprintf(&b, "    info:      %q\n", c.Info)
		if c.MetaErr != nil {
			fmt.Fprintf(&b, "    meta err:  %v\n", c.MetaErr)
		}
		fmt.Fprintf(&b, "    source:    %q\n", c.SourceText())
		fmt.Fprintf(&b, "    resultpos: %q\n", string(c.ResultPos.In(n.Bytes())))
		if len(c.Results) == 0 {
			fmt.Fprintf(&b, "    results:   none\n")
			continue
		}
		for j, r := range c.Results {
			fmt.Fprintf(&b, "    result[%d]: form=%s", j, r.Form)
			if r.Dest != "" {
				fmt.Fprintf(&b, " dest=%s", r.Dest)
			}
			if r.MetaErr != nil {
				fmt.Fprintf(&b, " metaErr=%v", r.MetaErr)
			} else if r.Meta != nil {
				var kv []string
				for _, e := range r.Meta.Entries() {
					if e.Flag {
						kv = append(kv, e.Key)
						continue
					}
					kv = append(kv, e.Key+"="+e.Value)
				}
				if len(kv) > 0 {
					fmt.Fprintf(&b, " meta=[%s]", strings.Join(kv, " "))
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// fixedRun is the timestamp every corpus splice uses, so goldens are deterministic.
var fixedRun = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

// TestCorpusSplice covers §11.5 end to end: every runnable cell in every corpus file
// has its result replaced in one Apply, and the whole document is compared to a
// golden. This is the write path over the entire corpus at once.
//
// Cells whose source fence is unclosed are skipped and reported in the golden, since
// §4.2 gives them no result position.
func TestCorpusSplice(t *testing.T) {
	for _, path := range corpusFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readFile(t, path)
			n, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			var edits []Edit
			var skipped []string
			for _, c := range n.Cells() {
				block := ResultBlock{
					Form: ResultOutput,
					Body: "spliced result for " + c.HeadingText,
					Run:  fixedRun,
					Tool: "notefmt/0.0",
				}
				text, err := block.String()
				if err != nil {
					t.Fatalf("building result: %v", err)
				}
				edit, err := c.SetResult(text)
				if err != nil {
					skipped = append(skipped, fmt.Sprintf("%s: %v", c.HeadingText, err))
					continue
				}
				edits = append(edits, edit)
			}

			out, err := n.Apply(edits...)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got := string(out)
			if len(skipped) > 0 {
				got += "\n=== cells whose result could not be persisted ===\n"
				for _, s := range skipped {
					got += s + "\n"
				}
			}
			checkGolden(t, strings.TrimSuffix(path, ".md")+".spliced", got)

			// Prose outside every result position must be untouched: verify by
			// re-parsing and comparing each cell's source and heading.
			n2, err := Parse(out)
			if err != nil {
				t.Fatalf("spliced output no longer parses: %v", err)
			}
			if len(n2.Cells()) != len(n.Cells()) {
				t.Fatalf("cell count changed: %d -> %d", len(n.Cells()), len(n2.Cells()))
			}
			for i, c := range n.Cells() {
				if n2.Cells()[i].SourceText() != c.SourceText() {
					t.Errorf("cell %d source changed", i)
				}
				if n2.Cells()[i].HeadingText != c.HeadingText {
					t.Errorf("cell %d heading changed", i)
				}
			}
		})
	}
}

// TestCorpusSpliceIsConvergent pins the volatile lifecycle over the whole corpus:
// running every cell twice with the same result reaches a fixed point. Without this,
// every run would grow the file.
func TestCorpusSpliceIsConvergent(t *testing.T) {
	for _, path := range corpusFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			first := spliceAll(t, readFile(t, path))
			second := spliceAll(t, first)
			if string(first) != string(second) {
				t.Errorf("splicing twice is not convergent for %s", path)
			}
		})
	}
}

func spliceAll(t *testing.T, src []byte) []byte {
	t.Helper()
	n, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var edits []Edit
	for _, c := range n.Cells() {
		block := ResultBlock{
			Form: ResultOutput,
			Body: "spliced result for " + c.HeadingText,
			Run:  fixedRun,
			Tool: "notefmt/0.0",
		}
		text, err := block.String()
		if err != nil {
			t.Fatalf("building result: %v", err)
		}
		edit, err := c.SetResult(text)
		if err != nil {
			continue
		}
		edits = append(edits, edit)
	}
	out, err := n.Apply(edits...)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out
}

// TestCorpusAssignID covers §11.7 over the corpus: assigning an identity to every
// cell that lacks one is append-only, and the result still parses with every id
// readable and every fence body unchanged.
func TestCorpusAssignID(t *testing.T) {
	for _, path := range corpusFiles(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readFile(t, path)
			n, err := Parse(src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			var edits []Edit
			// Indexed, not keyed by heading: two cells may legitimately share a
			// heading (and therefore a slug), which is the whole point of
			// identity.md — a map would silently collapse them.
			want := make([]string, len(n.Cells()))
			for i, c := range n.Cells() {
				if c.ID != "" {
					want[i] = c.ID
					continue
				}
				id := testID(i)
				edit, err := c.AssignID(id)
				if err != nil {
					t.Fatalf("AssignID(%q) for %q: %v", id, c.HeadingText, err)
				}
				edits = append(edits, edit)
				want[i] = id
			}
			if len(edits) == 0 {
				return
			}

			out, err := n.Apply(edits...)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			n2, err := Parse(out)
			if err != nil {
				t.Fatalf("output no longer parses: %v", err)
			}
			if len(n2.Cells()) != len(n.Cells()) {
				t.Fatalf("cell count changed: %d -> %d", len(n.Cells()), len(n2.Cells()))
			}
			for i, c := range n2.Cells() {
				if c.ID != want[i] {
					t.Errorf("cell %d (%q) id = %q, want %q", i, c.HeadingText, c.ID, want[i])
				}
				if c.SourceText() != n.Cells()[i].SourceText() {
					t.Errorf("cell %d source changed", i)
				}
			}
			// Nothing but info strings grew, so the body count is unchanged.
			if len(out) <= len(src) {
				t.Errorf("document did not grow")
			}
		})
	}
}

// testID encodes i as a valid id: base32 digits, left-padded with 'a'. Distinct
// indices give distinct ids, which duplicate-id rejection (§5.1) requires.
func testID(i int) string {
	out := []byte("aaaaaaaa")
	for k := IDLen - 1; k >= 0 && i > 0; k-- {
		out[k] = idAlphabet[i%len(idAlphabet)]
		i /= len(idAlphabet)
	}
	return string(out)
}
