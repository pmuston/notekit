package notetool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmuston/notekit/doc"
)

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

		// The drift this package exists to prevent. clinote's executor claims "sh" and
		// package run compares tags for equality, so clinote refuses `bash` cells as
		// surely as sqlnote does. The old hand-maintained hint mapped bash to clinote
		// and would have sent the user to a second refusal.
		{"bash is claimed by nothing", []string{"bash"}, "sqlnote", ""},
		{"zsh is claimed by nothing", []string{"zsh"}, "sqlnote", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sibling(tc.langs, tc.self); got != tc.want {
				t.Errorf("Sibling(%v, %q) = %q, want %q", tc.langs, tc.self, got, tc.want)
			}
		})
	}
}

// TestToolsAreConsistent guards the registry's own shape. The per-tool Lang values are
// checked against the executors in each cmd package, which is the only place that can
// import them.
func TestToolsAreConsistent(t *testing.T) {
	seenName := map[string]bool{}
	seenLang := map[string]bool{}
	for _, tool := range Tools {
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
			t.Errorf("two tools claim %q; Sibling cannot choose between them, and "+
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
		if err := CheckEngine(sqlOnly, "sqlnote", "sql"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	// Per cell is what package run enforces, so one runnable cell is enough. Refusing the
	// whole file would be stricter than the format.
	t.Run("accepts a partial match", func(t *testing.T) {
		if err := CheckEngine(mixed, "sqlnote", "sql"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
		if err := CheckEngine(mixed, "clinote", "sh"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	// Nothing to contradict. A notebook Create made always has a cell, so this is the
	// half-written case rather than a broken one.
	t.Run("accepts a cell-less notebook", func(t *testing.T) {
		if err := CheckEngine(bare, "sqlnote", "sql"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	t.Run("refuses and points at the sibling", func(t *testing.T) {
		err := CheckEngine(sqlOnly, "clinote", "sh")
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
		err := CheckEngine(foreign, "sqlnote", "sql")
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), "try:") {
			t.Errorf("no tool runs python, so there should be no suggestion: %v", err)
		}
	})

	t.Run("reports a non-notebook as such", func(t *testing.T) {
		notNotebook := write("plain.md", "# just markdown\n")
		if err := CheckEngine(notNotebook, "sqlnote", "sql"); err == nil {
			t.Error("want an error for a file without notekit front matter")
		}
	})

	t.Run("reports a missing file", func(t *testing.T) {
		if err := CheckEngine(filepath.Join(dir, "nope.md"), "sqlnote", "sql"); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("both directions of the real pair", func(t *testing.T) {
		if err := CheckEngine(shOnly, "sqlnote", "sql"); err == nil {
			t.Error("sqlnote should refuse a shell notebook")
		}
		if err := CheckEngine(shOnly, "clinote", "sh"); err != nil {
			t.Errorf("clinote should accept a shell notebook: %v", err)
		}
	})
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()

	t.Run("writes a parseable notebook with one cell", func(t *testing.T) {
		path := filepath.Join(dir, "parts-list.md")
		cell := doc.NewCell{Heading: "First query", Lang: "sql", Body: "SELECT 1;\n"}
		if err := Create(path, cell); err != nil {
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
		if err := CheckEngine(path, "sqlnote", "sql"); err != nil {
			t.Errorf("a notebook Create just made must be runnable by its maker: %v", err)
		}
	})

	t.Run("refuses to overwrite", func(t *testing.T) {
		path := filepath.Join(dir, "keep.md")
		const precious = "work I would hate to lose\n"
		if err := os.WriteFile(path, []byte(precious), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Create(path, doc.NewCell{Heading: "H", Lang: "sql"}); err == nil {
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
		if err := Create(filepath.Join(dir, "no-such-dir", "x.md"),
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
