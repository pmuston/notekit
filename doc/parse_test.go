package doc

import (
	"strings"
	"testing"
)

func TestFrontMatter(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		title   string
		fmBytes string
	}{
		{
			name:    "minimal",
			src:     "---\nnotekit: 1\n---\n",
			fmBytes: "---\nnotekit: 1\n---\n",
		},
		{
			name:    "with title",
			src:     "---\nnotekit: 1\ntitle: My Notebook\n---\n",
			title:   "My Notebook",
			fmBytes: "---\nnotekit: 1\ntitle: My Notebook\n---\n",
		},
		{
			name:    "double-quoted title",
			src:     "---\nnotekit: 1\ntitle: \"Quoted: Title\"\n---\n",
			title:   "Quoted: Title",
			fmBytes: "---\nnotekit: 1\ntitle: \"Quoted: Title\"\n---\n",
		},
		{
			name:    "single-quoted title",
			src:     "---\nnotekit: 1\ntitle: 'Single'\n---\n",
			title:   "Single",
			fmBytes: "---\nnotekit: 1\ntitle: 'Single'\n---\n",
		},
		{
			// Every key but the two reserved ones is passthrough, preserved
			// byte-for-byte and never interpreted (§2).
			name: "passthrough keys including nested structures",
			src: "---\nnotekit: 1\nclinote-session: shell\nsqlnote-db: ./x.db\n" +
				"nested:\n  a: 1\n  b: [2, 3]\nlist:\n  - one\n  - two\n---\n",
			fmBytes: "---\nnotekit: 1\nclinote-session: shell\nsqlnote-db: ./x.db\n" +
				"nested:\n  a: 1\n  b: [2, 3]\nlist:\n  - one\n  - two\n---\n",
		},
		{
			name:    "extra whitespace around scalar",
			src:     "---\nnotekit:   1  \ntitle:   Spaced  \n---\n",
			title:   "Spaced",
			fmBytes: "---\nnotekit:   1  \ntitle:   Spaced  \n---\n",
		},
		{
			// A nested key named `title` must not be mistaken for the top-level
			// one, which is why only indent-zero lines are read.
			name:    "nested title is ignored",
			src:     "---\nnotekit: 1\nmeta:\n  title: Nested\n---\n",
			title:   "",
			fmBytes: "---\nnotekit: 1\nmeta:\n  title: Nested\n---\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, tt.src)
			if n.Version() != Version {
				t.Errorf("Version() = %d, want %d", n.Version(), Version)
			}
			if n.Title() != tt.title {
				t.Errorf("Title() = %q, want %q", n.Title(), tt.title)
			}
			if got := string(n.FrontMatter().In(n.Bytes())); got != tt.fmBytes {
				t.Errorf("front matter span = %q, want %q", got, tt.fmBytes)
			}
			if got := string(n.Bytes()); got != tt.src {
				t.Errorf("round trip changed bytes")
			}
		})
	}
}

func TestNotANotebook(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"empty file", ""},
		{"no front matter", "## H\n\n```sh\na\n```\n"},
		{"front matter not at start", "text\n---\nnotekit: 1\n---\n"},
		{"unterminated front matter", "---\nnotekit: 1\n"},
		{"no notekit key", "---\ntitle: T\n---\n"},
		{"notekit not an integer", "---\nnotekit: one\n---\n"},
		{"unsupported version", "---\nnotekit: 2\n---\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src))
			var nn *NotNotebookError
			if !asError(err, &nn) {
				t.Fatalf("Parse = %v, want *NotNotebookError", err)
			}
			// §2 requires refusal, and the message should say why.
			if !strings.Contains(err.Error(), "not a notekit notebook") {
				t.Errorf("error message = %q", err.Error())
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name    string
		heading string
		want    string
	}{
		{"simple", "Disk usage", "disk-usage"},
		{"mixed case", "Disk Usage By Directory", "disk-usage-by-directory"},
		{"punctuation collapses", "Disk usage: by top-level directory!", "disk-usage-by-top-level-directory"},
		{"leading and trailing punctuation trimmed", "  ...Hello!!  ", "hello"},
		{"digits kept", "Top 5 tables (2026)", "top-5-tables-2026"},
		{"underscores are separators", "snake_case_name", "snake-case-name"},
		{"runs collapse to one dash", "a   ---   b", "a-b"},
		{"non-ascii yields empty", "日本語", ""},
		{"emoji yields empty", "⚙️", ""},
		{"percent only yields empty", "100%", "100"},
		{"empty heading", "", ""},
		{"accented letters are not ascii", "Café", "caf"},
		{
			// §5.2: bounded at 60 characters, then any trailing dash trimmed, so
			// the resulting filename stays well inside filesystem limits.
			name:    "truncated at 60",
			heading: strings.Repeat("ab", 40),
			want:    strings.Repeat("ab", 30),
		},
		{
			name:    "truncation does not leave a trailing dash",
			heading: strings.Repeat("a", 59) + " tail",
			want:    strings.Repeat("a", 59),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slug(tt.heading)
			if got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.heading, got, tt.want)
			}
			if len(got) > SlugMaxLen {
				t.Errorf("Slug(%q) is %d bytes, over the %d limit", tt.heading, len(got), SlugMaxLen)
			}
		})
	}
}

// TestSharedSlugIsNotACollision pins §5.2: two cells may share a slug, and neither
// gets a positional suffix. Suffixes derived from document order are exactly what
// made the superseded scheme reassign artifacts on reorder.
func TestSharedSlugIsNotACollision(t *testing.T) {
	body := "## Results\n\n```sh\na\n```\n\n## Results\n\n```sh\nb\n```\n"
	n := mustParse(t, front+body)
	cells := n.Cells()
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(cells))
	}
	if cells[0].Slug != "results" || cells[1].Slug != "results" {
		t.Errorf("slugs = %q, %q; both should be %q with no suffix",
			cells[0].Slug, cells[1].Slug, "results")
	}
}

func TestValidID(t *testing.T) {
	valid := []string{"k3m7q2vf", "aaaaaaaa", "22222222", "abcdefgh", "zzzzzz77"}
	invalid := []string{"", "k3m7q2v", "k3m7q2vff", "K3M7Q2VF", "k3m7q2v1", "k3m7q2v0", "k3m7q2v8", "k3m7q2v9", "k3-7q2vf"}

	for _, s := range valid {
		if !ValidID(s) {
			t.Errorf("ValidID(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true, want false", s)
		}
	}
}

func TestNewIDIsValid(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !ValidID(id) {
			t.Fatalf("NewID produced %q, which ValidID rejects", id)
		}
		seen[id] = true
	}
	// Not a statistical test — just a smoke check that ids are not constant.
	if len(seen) < 190 {
		t.Errorf("only %d distinct ids in 200 draws", len(seen))
	}
}

// TestIDNotAssignedToInlineResultCell pins the laziness rule (§5.1): most cells
// never acquire an id, so most source fences stay clean.
func TestIDNotAssignedToInlineResultCell(t *testing.T) {
	body := "## H\n\n```sh\na\n```\n\n```output\nr\n```\n"
	n := mustParse(t, front+body)
	if got := n.Cells()[0].ID; got != "" {
		t.Errorf("ID = %q, want empty — an inline result needs no identity", got)
	}
	if strings.Contains(string(n.Bytes()), "id=") {
		t.Error("source fence gained an id token without being asked")
	}
}

func TestLine(t *testing.T) {
	src := front + "## One\n\n```sh\na\n```\n\n## Two\n\n```sh\nb\n```\n"
	n := mustParse(t, src)

	// Front matter is 4 lines, then a blank, so the first heading is line 6.
	if got := n.Line(n.Cells()[0].Heading.Start); got != 6 {
		t.Errorf("first heading on line %d, want 6", got)
	}
	if got := n.Line(n.Cells()[1].Heading.Start); got != 12 {
		t.Errorf("second heading on line %d, want 12", got)
	}
	if got := n.Line(0); got != 1 {
		t.Errorf("offset 0 on line %d, want 1", got)
	}
	// An offset past the end clamps rather than panicking, so a caller reporting a
	// span at end of file cannot crash a tool.
	if got, want := n.Line(len(src)+100), n.Line(len(src)); got != want {
		t.Errorf("clamped line = %d, want %d", got, want)
	}
}
