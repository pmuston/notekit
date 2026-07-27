package notetool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmuston/notekit/doc"
)

// pair mirrors what a module supplies as Peers. The package under test ships no list of its
// own — that is the point of the promotion — so its tests declare one.
var pair = []Peer{{Name: "clinote", Lang: "sh"}, {Name: "sqlnote", Lang: "sql"}}

func clinote() Tool { return Tool{Name: "clinote", Lang: "sh", Peers: pair} }
func sqlnote() Tool { return Tool{Name: "sqlnote", Lang: "sql", Peers: pair} }

func TestSiblingNamesTheOtherTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		langs []string
		self  string
		want  string
	}{
		{"sql notebook seen by clinote", []string{"sql"}, "clinote", "sqlnote"},
		{"sh notebook seen by sqlnote", []string{"sh"}, "sqlnote", "clinote"},
		{"never suggests itself", []string{"sh"}, "clinote", ""},
		{"first match wins", []string{"sql", "sh"}, "clinote", "sqlnote"},
		{"nothing for an unclaimed tag", []string{"python"}, "clinote", ""},
		{"nothing for no cells", nil, "clinote", ""},
		// A tool in its own repository knows of nobody, which is supported rather than
		// degraded: Suggest then falls back to the notebook's advisory key.
		{"no peers at all", []string{"sql"}, "rednote", ""},

		// The drift this package exists to prevent. clinote's executor claims "sh" and
		// package run compares tags for equality, so clinote refuses `bash` cells as
		// surely as sqlnote does. The old hand-maintained hint mapped bash to clinote
		// and would have sent the user to a second refusal.
		{"bash is claimed by nothing", []string{"bash"}, "sqlnote", ""},
		{"zsh is claimed by nothing", []string{"zsh"}, "sqlnote", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			self := Tool{Name: tc.self, Peers: pair}
			if tc.self == "rednote" {
				self = Tool{Name: "rednote", Lang: "redis"} // no peers
			}
			if got := self.sibling(tc.langs); got != tc.want {
				t.Errorf("sibling(%v) as %q = %q, want %q", tc.langs, tc.self, got, tc.want)
			}
		})
	}
}

// TestPeerListIsConsistent guards the shape a caller's Peers list must have. The per-tool
// Lang values are checked against the executors in each cmd package, which is the only place
// that can import them.
func TestPeerListIsConsistent(t *testing.T) {
	seenName := map[string]bool{}
	seenLang := map[string]bool{}
	for _, tool := range pair {
		if tool.Name == "" || tool.Lang == "" {
			t.Errorf("incomplete entry: %+v", tool)
		}
		if seenName[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		// Two tools claiming one tag would make Sibling's answer depend on slice order,
		// and is the case format spec §2.1 says to revisit the design over rather than
		// paper around.
		if seenLang[tool.Lang] {
			t.Errorf("two tools claim %q; sibling cannot choose between them, and "+
				"§2.1 says a shared tag needs a design decision, not a tie-break",
				tool.Lang)
		}
		seenName[tool.Name] = true
		seenLang[tool.Lang] = true
	}
}

func TestCheckEngine(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	sqlOnly := write("sql.md", "---\nnotekit: 1\n---\n\n## a\n\n```sql\nSELECT 1;\n```\n")
	shOnly := write("sh.md", "---\nnotekit: 1\n---\n\n## a\n\n```sh\nls\n```\n")
	mixed := write("mixed.md", "---\nnotekit: 1\n---\n\n## a\n\n```sql\nSELECT 1;\n```\n\n## b\n\n```sh\nls\n```\n")
	bare := write("bare.md", "---\nnotekit: 1\n---\n\njust prose\n")
	foreign := write("py.md", "---\nnotekit: 1\n---\n\n## a\n\n```python\nprint(1)\n```\n")

	t.Run("accepts a notebook it can run", func(t *testing.T) {
		if err := sqlnote().CheckEngine(sqlOnly); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	// Per cell is what package run enforces, so one runnable cell is enough. Refusing the
	// whole file would be stricter than the format.
	t.Run("accepts a partial match", func(t *testing.T) {
		if err := sqlnote().CheckEngine(mixed); err != nil {
			t.Errorf("want nil, got %v", err)
		}
		if err := clinote().CheckEngine(mixed); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	// Nothing to contradict. A notebook Create made always has a cell, so this is the
	// half-written case rather than a broken one.
	t.Run("accepts a cell-less notebook", func(t *testing.T) {
		if err := sqlnote().CheckEngine(bare); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	t.Run("refuses and points at the sibling", func(t *testing.T) {
		err := clinote().CheckEngine(sqlOnly)
		if err == nil {
			t.Fatal("want an error")
		}
		for _, want := range []string{"sql", "clinote", "try: sqlnote"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q: %v", want, err)
			}
		}
	})

	// No tool claims python, so there is nowhere to send anyone. Saying nothing beats
	// naming a tool that would refuse in turn.
	t.Run("refuses without a suggestion it cannot make", func(t *testing.T) {
		err := sqlnote().CheckEngine(foreign)
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), "try:") {
			t.Errorf("no tool runs python, so there should be no suggestion: %v", err)
		}
	})

	t.Run("reports a non-notebook as such", func(t *testing.T) {
		notNotebook := write("plain.md", "# just markdown\n")
		if err := sqlnote().CheckEngine(notNotebook); err == nil {
			t.Error("want an error for a file without notekit front matter")
		}
	})

	t.Run("reports a missing file", func(t *testing.T) {
		if err := sqlnote().CheckEngine(filepath.Join(dir, "nope.md")); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("both directions of the real pair", func(t *testing.T) {
		if err := sqlnote().CheckEngine(shOnly); err == nil {
			t.Error("sqlnote should refuse a shell notebook")
		}
		if err := clinote().CheckEngine(shOnly); err != nil {
			t.Errorf("clinote should accept a shell notebook: %v", err)
		}
	})
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()

	t.Run("writes a parseable notebook with one cell", func(t *testing.T) {
		path := filepath.Join(dir, "parts-list.md")
		cell := doc.NewCell{Heading: "First query", Lang: "sql", Body: "SELECT 1;\n"}
		if err := sqlnote().Create(path, cell); err != nil {
			t.Fatalf("Create: %v", err)
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		nb, err := doc.Parse(src)
		if err != nil {
			t.Fatalf("unparseable: %v\n%s", err, src)
		}
		if got := nb.Title(); got != "Parts list" {
			t.Errorf("Title() = %q, want %q", got, "Parts list")
		}
		cells := nb.Cells()
		if len(cells) != 1 {
			t.Fatalf("got %d cells, want 1", len(cells))
		}
		if len(cells[0].Results) != 0 {
			t.Error("a new cell must carry no result (§10 f)")
		}
		// The whole reason the cell is mandatory: without it there is no tag, and
		// CheckEngine could not tell what the notebook is.
		if err := sqlnote().CheckEngine(path); err != nil {
			t.Errorf("a notebook Create just made must be runnable by its maker: %v", err)
		}
	})

	t.Run("refuses to overwrite", func(t *testing.T) {
		path := filepath.Join(dir, "keep.md")
		const precious = "work I would hate to lose\n"
		if err := os.WriteFile(path, []byte(precious), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := sqlnote().Create(path, doc.NewCell{Heading: "H", Lang: "sql"}); err == nil {
			t.Fatal("want an error for an existing file")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != precious {
			t.Errorf("the existing file was modified: %q", got)
		}
	})

	t.Run("reports an unwritable directory", func(t *testing.T) {
		if err := sqlnote().Create(filepath.Join(dir, "no-such-dir", "x.md"),
			doc.NewCell{Heading: "H", Lang: "sql"}); err == nil {
			t.Error("want an error")
		}
	})
}

func TestTitleFromPath(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"parts-list.md", "Parts list"},
		{"my_shell_notes.md", "My shell notes"},
		{"notes.md", "Notes"},
		{"/a/b/deep-one.md", "Deep one"},
		{"already Capital.md", "Already Capital"},
		{"multi--dash.md", "Multi dash"},
		{"trailing-.md", "Trailing"},
		{".md", ""},
		{"UPPER.md", "UPPER"},
		{"ünïcode.md", "Ünïcode"},
	} {
		if got := TitleFromPath(tc.path); got != tc.want {
			t.Errorf("TitleFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestQuoteList(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"sql"}, `"sql"`},
		{[]string{"sql", "sh"}, `"sql" and "sh"`},
		{[]string{"sql", "sh", "py"}, `"sql", "sh" and "py"`},
	} {
		if got := quoteList(tc.in); got != tc.want {
			t.Errorf("quoteList(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// --- the advisory notekit-tool key ---------------------------------------------------

// parse is a helper for the key tests, which care about front matter rather than files.
func parse(t *testing.T, src string) *doc.Notebook {
	t.Helper()
	nb, err := doc.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return nb
}

func TestSuggestPrefersTheNotebooksOwnClaim(t *testing.T) {
	const sqlCell = "\n## a\n\n```sql\nSELECT 1;\n```\n"

	t.Run("a key naming a tool from another module is still usable", func(t *testing.T) {
		// The entire reason the key exists. priortool is a separate repository, so Tools
		// cannot name it and Sibling would have nothing to offer — but the file does.
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: priortool\n---\n"+
			"\n## a\n\n```cypher\nMATCH (n) RETURN n;\n```\n")
		if got := clinote().Suggest(nb); got != "priortool" {
			t.Errorf("Suggest = %q, want %q — the key is the only source that can name "+
				"a tool outside this module", got, "priortool")
		}
	})

	t.Run("falls back to the registry when there is no key", func(t *testing.T) {
		// Every notebook written before the key existed, and any written by hand.
		nb := parse(t, "---\nnotekit: 1\n---\n"+sqlCell)
		if got := clinote().Suggest(nb); got != "sqlnote" {
			t.Errorf("Suggest = %q, want %q", got, "sqlnote")
		}
	})

	t.Run("never suggests the tool already running", func(t *testing.T) {
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: clinote\n---\n"+sqlCell)
		// The key names us, so it is no help; fall through to what the cells imply.
		if got := clinote().Suggest(nb); got != "sqlnote" {
			t.Errorf("Suggest = %q, want %q", got, "sqlnote")
		}
	})
}

func TestToolKeyWarning(t *testing.T) {
	const sqlCell = "\n## a\n\n```sql\nSELECT 1;\n```\n"

	t.Run("warns when the key contradicts the cells", func(t *testing.T) {
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: clinote\n---\n"+sqlCell)
		warn := sqlnote().ToolKeyWarning(nb)
		if warn == "" {
			t.Fatal("want a warning: the key says clinote, the cells are sql")
		}
		for _, want := range []string{"clinote", "sql", "cells decide"} {
			if !strings.Contains(warn, want) {
				t.Errorf("warning should mention %q: %s", want, warn)
			}
		}
	})

	t.Run("silent when the key agrees", func(t *testing.T) {
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: sqlnote\n---\n"+sqlCell)
		if warn := sqlnote().ToolKeyWarning(nb); warn != "" {
			t.Errorf("want silence, got %q", warn)
		}
	})

	t.Run("silent for a tool it cannot check", func(t *testing.T) {
		// An unknown tool has no lang to compare, and guessing would produce a warning
		// about a tool that may be perfectly correct. This is the key's whole purpose.
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: priortool\n---\n"+sqlCell)
		if warn := sqlnote().ToolKeyWarning(nb); warn != "" {
			t.Errorf("an unknown tool cannot be contradicted, got %q", warn)
		}
	})

	t.Run("silent with no key and with no cells", func(t *testing.T) {
		if warn := sqlnote().ToolKeyWarning(parse(t, "---\nnotekit: 1\n---\n"+sqlCell)); warn != "" {
			t.Errorf("no key, so nothing to say: %q", warn)
		}
		nb := parse(t, "---\nnotekit: 1\nnotekit-tool: clinote\n---\n\njust prose\n")
		if warn := sqlnote().ToolKeyWarning(nb); warn != "" {
			t.Errorf("no cells, so nothing to contradict: %q", warn)
		}
	})
}

// TestInspectNeverRefusesOverTheKey is the load-bearing guarantee of §2.1: the key is
// advisory, so a wrong value must cost a warning and nothing more. If this ever fails, a
// stale key has become able to stop a notebook opening — which is precisely the failure the
// advisory framing exists to prevent.
func TestInspectNeverRefusesOverTheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "n.md")
	// The key is flatly wrong: it names clinote, the cells are sql, and sqlnote is asking.
	src := "---\nnotekit: 1\nnotekit-tool: clinote\n---\n\n## a\n\n```sql\nSELECT 1;\n```\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	warn, err := sqlnote().Inspect(path)
	if err != nil {
		t.Fatalf("the key must not cause a refusal: %v", err)
	}
	if warn == "" {
		t.Error("want a warning about the contradicting key")
	}
}

func TestCreateWritesTheAdvisoryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	if err := sqlnote().Create(path, doc.NewCell{Heading: "H", Lang: "sql", Body: "SELECT 1;\n"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := doc.Parse(src)
	if err != nil {
		t.Fatalf("unparseable: %v\n%s", err, src)
	}
	if got := nb.Front()[doc.FrontKeyTool]; got != "sqlnote" {
		t.Errorf("%s = %q, want %q\n%s", doc.FrontKeyTool, got, "sqlnote", src)
	}
	// What it writes must not warn about itself.
	if warn := sqlnote().ToolKeyWarning(nb); warn != "" {
		t.Errorf("a freshly created notebook must not warn: %s", warn)
	}
}

// --- the notebook picker ---------------------------------------------------------------

const frontMatter = "---\nnotekit: 1\n---\n\n"

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindNotebooks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "one.md", frontMatter+"## A\n\n```sh\necho x\n```\n")
	write(t, dir, "two.md", "---\nnotekit: 1\n---\n\nprose\n")
	// Not candidates: no front matter, a version this build does not define, not
	// markdown, and a directory that merely ends in .md.
	write(t, dir, "plain.md", "# Just markdown\n")
	write(t, dir, "future.md", "---\nnotekit: 2\n---\n")
	write(t, dir, "notes.txt", frontMatter+"## A\n\n```sh\nx\n```\n")
	if err := os.Mkdir(filepath.Join(dir, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := FindNotebooks(dir)
	if err != nil {
		t.Fatalf("FindNotebooks: %v", err)
	}
	var names []string
	for _, p := range found {
		names = append(names, filepath.Base(p))
	}
	want := []string{"one.md", "two.md"}
	if len(names) != len(want) {
		t.Fatalf("found %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("found[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestFindNotebooksMissingDir(t *testing.T) {
	if _, err := FindNotebooks(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("want an error")
	}
}

// TestResolvePicker covers the no-argument path. The picker is deliberately not
// interactive: with one candidate the answer is obvious, and with several the useful thing
// is to name them rather than guess.
func TestResolvePicker(t *testing.T) {
	t.Run("exactly one is chosen", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "only.md", frontMatter+"## A\n\n```sh\nx\n```\n")
		got, err := Resolve("", dir)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if filepath.Base(got) != "only.md" {
			t.Errorf("got %q, want only.md", got)
		}
	})

	t.Run("several are listed rather than guessed", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "a.md", frontMatter+"## A\n\n```sh\nx\n```\n")
		write(t, dir, "b.md", frontMatter+"## B\n\n```sh\nx\n```\n")
		_, err := Resolve("", dir)
		if err == nil {
			t.Fatal("want an error naming the candidates")
		}
		for _, want := range []string{"a.md", "b.md"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("none explains what to do", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "plain.md", "# not a notebook\n")
		_, err := Resolve("", dir)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "notekit: 1") {
			t.Errorf("err = %v, want it to say how to make one", err)
		}
	})

	// The message has to read correctly for both the caller's "." and a real path, since
	// the directory is a parameter only so tests need not chdir.
	t.Run("names the directory it looked in", func(t *testing.T) {
		empty := t.TempDir()
		err := resolveErr(t, "", empty)
		if !strings.Contains(err.Error(), empty) {
			t.Errorf("err = %v, want it to name %q", err, empty)
		}
		if err := resolveErr(t, "", "."); !strings.Contains(err.Error(), "current directory") {
			// "." is what every caller passes, and "no notebooks in ." reads badly.
			t.Errorf("err = %v, want it to say \"the current directory\"", err)
		}
	})
}

func TestResolveExplicitPath(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.md", frontMatter+"## A\n\n```sh\nx\n```\n")
	bad := write(t, dir, "bad.md", "# not a notebook\n")

	if got, err := Resolve(good, dir); err != nil || got != good {
		t.Errorf("Resolve(%q) = %q, %v", good, got, err)
	}
	// A named file that is not a notebook is refused with the reason, rather than silently
	// falling back to the picker and opening something else.
	if err := resolveErr(t, bad, dir); !strings.Contains(err.Error(), "not a notekit notebook") {
		t.Errorf("err = %v", err)
	}
	if _, err := Resolve(filepath.Join(dir, "nope.md"), dir); err == nil {
		t.Error("want an error for a missing file")
	}
}

func resolveErr(t *testing.T, arg, dir string) error {
	t.Helper()
	_, err := Resolve(arg, dir)
	if err == nil {
		t.Fatalf("Resolve(%q, %q) = nil error", arg, dir)
	}
	return err
}
