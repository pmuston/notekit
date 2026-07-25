package doc

import "testing"

func TestFenceOpener(t *testing.T) {
	tests := []struct {
		line   string
		ok     bool
		ch     byte
		n      int
		infoAt int
	}{
		{line: "```", ok: true, ch: '`', n: 3, infoAt: 3},
		{line: "```sh", ok: true, ch: '`', n: 3, infoAt: 3},
		{line: "````sh", ok: true, ch: '`', n: 4, infoAt: 4},
		{line: "~~~sh", ok: true, ch: '~', n: 3, infoAt: 3},
		{line: "   ```sh", ok: true, ch: '`', n: 3, infoAt: 6},
		// Four spaces is indented code, not a fence.
		{line: "    ```sh", ok: false},
		{line: "``", ok: false},
		{line: "~~", ok: false},
		{line: "", ok: false},
		{line: "   ", ok: false},
		{line: "text", ok: false},
		// A backtick fence's info string may not contain a backtick, which is
		// what stops an inline code span from opening a fence.
		{line: "```sh `x`", ok: false},
		// A tilde fence's info string may contain backticks.
		{line: "~~~sh `x`", ok: true, ch: '~', n: 3, infoAt: 3},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			ch, n, infoAt, ok := fenceOpener([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if ch != tt.ch || n != tt.n || infoAt != tt.infoAt {
				t.Errorf("got ch=%q n=%d infoAt=%d, want ch=%q n=%d infoAt=%d",
					ch, n, infoAt, tt.ch, tt.n, tt.infoAt)
			}
		})
	}
}

func TestFenceCloser(t *testing.T) {
	tests := []struct {
		line string
		ch   byte
		n    int
		want bool
	}{
		{line: "```", ch: '`', n: 3, want: true},
		{line: "````", ch: '`', n: 3, want: true}, // longer closes shorter
		{line: "```", ch: '`', n: 4, want: false}, // shorter cannot close longer
		{line: "~~~", ch: '`', n: 3, want: false}, // wrong character
		{line: "   ```", ch: '`', n: 3, want: true},
		{line: "    ```", ch: '`', n: 3, want: false}, // indented code
		{line: "```  ", ch: '`', n: 3, want: true},    // trailing whitespace allowed
		{line: "``` x", ch: '`', n: 3, want: false},   // trailing content not allowed
		{line: "", ch: '`', n: 3, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := fenceCloser([]byte(tt.line), tt.ch, tt.n); got != tt.want {
				t.Errorf("fenceCloser(%q, %q, %d) = %v, want %v", tt.line, tt.ch, tt.n, got, tt.want)
			}
		})
	}
}

func TestMatchHeading(t *testing.T) {
	tests := []struct {
		line  string
		ok    bool
		level int
		text  string
	}{
		{line: "# One", ok: true, level: 1, text: "One"},
		{line: "## Two", ok: true, level: 2, text: "Two"},
		{line: "###### Six", ok: true, level: 6, text: "Six"},
		{line: "####### Seven", ok: false}, // seven hashes is not a heading
		{line: "##", ok: true, level: 2, text: ""},
		{line: "## ", ok: true, level: 2, text: ""},
		{line: "   ## Indented", ok: true, level: 2, text: "Indented"},
		{line: "    ## Code", ok: false}, // four spaces is indented code
		{line: "##NoSpace", ok: false},   // hashes must be followed by whitespace
		{line: "## Closing ##", ok: true, level: 2, text: "Closing"},
		{line: "## Closing##", ok: true, level: 2, text: "Closing##"}, // no space: literal
		{line: "## ###", ok: true, level: 2, text: ""},                // all hashes
		{line: "text", ok: false},
		{line: "", ok: false},
		{line: "## Tabbed\tname", ok: true, level: 2, text: "Tabbed\tname"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			h := matchHeading([]byte(tt.line))
			if (h != nil) != tt.ok {
				t.Fatalf("matchHeading(%q) = %v, want ok=%v", tt.line, h, tt.ok)
			}
			if h == nil {
				return
			}
			if h.level != tt.level || h.text != tt.text {
				t.Errorf("got level=%d text=%q, want level=%d text=%q", h.level, h.text, tt.level, tt.text)
			}
		})
	}
}

func TestProvenanceBody(t *testing.T) {
	tests := []struct {
		line  string
		ok    bool
		attrs string
	}{
		{line: "<!-- notekit:result kind=graph -->", ok: true, attrs: "kind=graph"},
		{line: "<!-- notekit:result -->", ok: true, attrs: ""},
		{line: "  <!-- notekit:result kind=graph -->  ", ok: true, attrs: "kind=graph"},
		{line: "<!--notekit:result kind=graph-->", ok: true, attrs: "kind=graph"},
		// The marker must be a whole token, so a longer word is not a match.
		{line: "<!-- notekit:results kind=graph -->", ok: false},
		{line: "<!-- just a note -->", ok: false},
		{line: "<!-- -->", ok: false},
		{line: "<!--", ok: false},
		{line: "", ok: false},
		{line: "notekit:result kind=graph", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			attrs, ok := provenanceBody([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && attrs != tt.attrs {
				t.Errorf("attrs = %q, want %q", attrs, tt.attrs)
			}
		})
	}
}

func TestImageDest(t *testing.T) {
	tests := []struct{ line, want string }{
		{"![alt](pic.png)", "pic.png"},
		{"  ![alt](pic.png)  ", "pic.png"},
		{"![](pic.png)", "pic.png"},
		{`![alt](pic.png "title")`, "pic.png"},
		{"![alt](n.assets/slug--k3m7q2vf.png)", "n.assets/slug--k3m7q2vf.png"},
		{"![alt]()", ""},
		{"![alt]", ""},
		{"not an image", ""},
		{"", ""},
		{"text ![alt](pic.png) more", ""}, // must be the whole line
		{"![abc)", ""},                    // bracketed but no "](" separator
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := imageDest([]byte(tt.line)); got != tt.want {
				t.Errorf("imageDest(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestSpanHelpers(t *testing.T) {
	s := Span{Start: 2, End: 5}
	if s.Len() != 3 {
		t.Errorf("Len() = %d, want 3", s.Len())
	}
	if s.Empty() {
		t.Error("Empty() = true for a 3-byte span")
	}
	if !(Span{4, 4}).Empty() {
		t.Error("Empty() = false for a zero-length span")
	}
	if !(Span{6, 4}).Empty() {
		t.Error("Empty() = false for an inverted span")
	}
	if got := string(s.In([]byte("abcdefg"))); got != "cde" {
		t.Errorf("In() = %q, want %q", got, "cde")
	}
}

func TestResultFormString(t *testing.T) {
	cases := map[ResultForm]string{
		ResultOutput:  "output",
		ResultError:   "error",
		ResultSidecar: "sidecar",
		ResultForm(9): "unknown",
	}
	for form, want := range cases {
		if got := form.String(); got != want {
			t.Errorf("ResultForm(%d).String() = %q, want %q", form, got, want)
		}
	}
}

func TestCellString(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no id, no result",
			body: "## Disk usage\n\n```sh\na\n```\n",
			want: "Disk usage\tsh\tid=-\tslug=disk-usage\tresult=none",
		},
		{
			name: "with id and sidecar",
			body: "## Wiring\n\n```cypher {id=k3m7q2vf}\na\n```\n\n" +
				"<!-- notekit:result kind=graph -->\n![x](n.assets/wiring--k3m7q2vf.png)\n",
			want: "Wiring\tcypher\tid=k3m7q2vf\tslug=wiring\tresult=sidecar",
		},
		{
			name: "several results are counted",
			body: "## H\n\n```sh\na\n```\n\n```output\n1\n```\n\n```output\n2\n```\n",
			want: "H\tsh\tid=-\tslug=h\tresult=output+1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			if got := n.Cells()[0].String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDuplicateIDErrorMessage(t *testing.T) {
	err := &DuplicateIDError{ID: "k3m7q2vf", Headings: [2]string{"One", "Two"}}
	want := `duplicate cell id "k3m7q2vf" on "One" and "Two" ` +
		`(delete one and re-run that cell to have a fresh id assigned)`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestFrontMatterCommentAndListLinesIgnored covers the scalar reader's skip
// branches: a comment, a sequence item, and a line with no colon are all
// passthrough content, not reserved keys.
func TestFrontMatterCommentAndListLinesIgnored(t *testing.T) {
	src := "---\n# a comment\nnotekit: 1\n- sequence item\nnocolon\ntitle: T\n---\n"
	n := mustParse(t, src)
	if n.Title() != "T" {
		t.Errorf("Title() = %q, want T", n.Title())
	}
	if got := string(n.FrontMatter().In(n.Bytes())); got != src {
		t.Errorf("front matter span = %q", got)
	}
}
