package doc

import (
	"bytes"
	"strings"
	"testing"
)

func TestScaffoldIsAParseableNotebookWithOneCell(t *testing.T) {
	src, err := Scaffold("Parts inventory", NewCell{
		Heading: "First query",
		Lang:    "sql",
		Body:    "SELECT 'hello' AS greeting;\n",
	})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	want := "---\nnotekit: 1\ntitle: Parts inventory\n---\n\n" +
		"## First query\n\n```sql\nSELECT 'hello' AS greeting;\n```\n"
	if string(src) != want {
		t.Errorf("Scaffold produced:\n%s\nwant:\n%s", src, want)
	}

	nb, err := Parse(src)
	if err != nil {
		t.Fatalf("the scaffold does not parse: %v", err)
	}
	if got := nb.Title(); got != "Parts inventory" {
		t.Errorf("Title() = %q", got)
	}
	// One cell, and it carries the engine's tag — which is the whole point: a notebook
	// with no cells would have no tag to derive an engine from (§2.1).
	cells := nb.Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	if cells[0].Lang != "sql" {
		t.Errorf("Lang = %q, want sql", cells[0].Lang)
	}
	// No invented result, per §10 f.
	if len(cells[0].Results) != 0 {
		t.Errorf("a new cell must have no result, got %+v", cells[0].Results)
	}
}

// TestScaffoldRoundTrips holds the format's central property over generated files too: a
// notebook a tool wrote must survive parse-and-reserialise untouched.
func TestScaffoldRoundTrips(t *testing.T) {
	for _, title := range []string{
		"Parts inventory", "", "true", "1.5", "Notes: a study", "# hash",
		`say "hi"`, "  padded  ", "-leading dash", "Ünïcøde", "no", "0x10",
	} {
		src, err := Scaffold(title, NewCell{Heading: "H", Lang: "sh", Body: "ls\n"})
		if err != nil {
			t.Fatalf("Scaffold(%q): %v", title, err)
		}
		nb, err := Parse(src)
		if err != nil {
			t.Fatalf("Scaffold(%q) is unparseable: %v\n%s", title, err, src)
		}
		if got := nb.Title(); got != strings.TrimSpace(title) && got != title {
			t.Errorf("Scaffold(%q): Title() = %q", title, got)
		}
		out, err := nb.Apply()
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !bytes.Equal(out, src) {
			t.Errorf("Scaffold(%q) does not round-trip:\n got %q\nwant %q", title, out, src)
		}
	}
}

func TestScaffoldRefusesANewlineInTheTitle(t *testing.T) {
	if _, err := Scaffold("two\nlines", NewCell{Heading: "H", Lang: "sh"}); err == nil {
		t.Fatal("want an error: a newline would end the front matter scalar early")
	}
}

func TestLangsAreDistinctInFirstAppearanceOrder(t *testing.T) {
	src := []byte("---\nnotekit: 1\n---\n\n" +
		"## a\n\n```sql\nSELECT 1;\n```\n\n" +
		"## b\n\n```sh\nls\n```\n\n" +
		"## c\n\n```sql\nSELECT 2;\n```\n" +
		"## d\n\nprose only, no fence\n")
	nb, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := nb.Langs()
	want := []string{"sql", "sh"}
	if len(got) != len(want) {
		t.Fatalf("Langs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Langs() = %v, want %v", got, want)
		}
	}
}

func TestLangsIsEmptyForACellLessNotebook(t *testing.T) {
	nb, err := Parse([]byte("---\nnotekit: 1\n---\n\njust prose\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// This is exactly the case a starter cell exists to prevent: nothing to derive an
	// engine from.
	if got := nb.Langs(); len(got) != 0 {
		t.Errorf("Langs() = %v, want none", got)
	}
}
