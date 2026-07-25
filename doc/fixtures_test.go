package doc

import "testing"

// front is the minimal valid front matter every fixture needs (§2).
const front = "---\nnotekit: 1\ntitle: Fixtures\n---\n\n"

// fixture is a notebook body paired with what it is meant to exercise.
type fixture struct {
	name string
	body string

	// goldmarkDiverges marks a body where this scanner and goldmark disagree by
	// design, so the cross-check (scan_goldmark_test.go) skips it. The only such
	// case is a fence nested in a list item: goldmark treats it as list content,
	// this scanner sees a top-level fence. Pathological in a notebook, and the
	// conservative outcome either way.
	goldmarkDiverges bool
}

var fixtures = []fixture{
	{name: "empty body", body: ""},
	{name: "prose only", body: "Just prose.\n\nTwo paragraphs.\n"},
	{
		name: "simple cell",
		body: "## Disk usage\n\n```sh\ndu -d1 -h\n```\n",
	},
	{
		name: "cell with output",
		body: "## Disk usage\n\n```sh {format=csv}\ndu -d1 -h\n```\n\n" +
			"```output {format=csv, run=\"2026-07-16T09:41:07Z\"}\nsize,path\n1.2G,./data\n```\n",
	},
	{
		name: "cell with error",
		body: "## Broken\n\n```sh\ndv\n```\n\n```error {status=127}\nzsh: command not found: dv\n```\n",
	},
	{
		name: "cell with sidecar reference",
		body: "## Module wiring\n\n```cypher {id=k3m7q2vf}\nMATCH (m) RETURN m\n```\n\n" +
			"<!-- notekit:result kind=graph, run=\"2026-07-16T10:02:55Z\" -->\n" +
			"![Module wiring](n.assets/module-wiring--k3m7q2vf.png)\n",
	},
	{
		name: "nested heading levels are separate cells",
		body: "## Outer\n\n```sh\na\n```\n\n### Inner\n\n```sh\nb\n```\n",
	},
	{
		name: "heading between fence and output breaks pairing",
		body: "## H\n\n```sh\na\n```\n\n### Sub\n\n```output\nr\n```\n",
	},
	{
		name: "inert example fence after source",
		body: "## H\n\n```sh\nreal\n```\n\nprose\n\n```sh\nexample only\n```\n",
	},
	{
		name: "untagged first fence suppresses the cell",
		body: "## H\n\n```\nplain\n```\n\n```sh\nnot a source fence\n```\n",
	},
	{
		name: "empty untagged fence suppresses the cell",
		body: "## H\n\n```\n```\n\n```sh\nnot a source fence\n```\n",
	},
	{
		name: "output-tagged first fence suppresses the cell",
		body: "## H\n\n```output\nstray\n```\n\n```sh\nnot a source fence\n```\n",
	},
	{
		name: "fence with no heading is not a cell",
		body: "```sh\nno heading\n```\n",
	},
	{
		name: "level 1 heading is not a cell",
		body: "# Title\n\n```sh\na\n```\n",
	},
	{
		name: "bare image is prose",
		body: "## H\n\n```sh\na\n```\n\nSee below.\n\n![plain](picture.png)\n",
	},
	{
		name: "provenance comment with no image is prose",
		body: "## H\n\n```sh\na\n```\n\n<!-- notekit:result kind=graph -->\n\nprose\n",
	},
	{
		name: "ordinary html comment is not provenance",
		body: "## H\n\n```sh\na\n```\n\n<!-- just a note -->\n![pic](p.png)\n",
	},
	{
		name: "two output fences both read as results",
		body: "## H\n\n```sh\na\n```\n\n```output\nfirst\n```\n\n```output\nsecond\n```\n",
	},
	{
		name: "mixed output and sidecar forms",
		body: "## H\n\n```sh {id=bbbb2345}\na\n```\n\n```output\ntext\n```\n\n" +
			"<!-- notekit:result kind=graph -->\n![x](n.assets/h--bbbb2345.png)\n",
	},
	{
		name: "paragraph terminates result position",
		body: "## H\n\n```sh\na\n```\n\n```output\nr\n```\n\nProse after the result.\n",
	},
	{
		name: "four backtick fence containing three",
		body: "## H\n\n````sh\n```\ninner\n```\n````\n",
	},
	{
		name: "tilde fence",
		body: "## H\n\n~~~sh {format=csv}\na\n~~~\n",
	},
	{
		name: "indented fence",
		body: "## H\n\n   ```sh\na\n   ```\n",
	},
	{
		name: "heading with closing hashes",
		body: "## Heading ##\n\n```sh\na\n```\n",
	},
	{
		name: "setext heading does not begin a section",
		body: "## Real\n\n```sh\na\n```\n\nUnderlined\n----------\n\n```sh\nstill inert\n```\n",
	},
	{
		name: "hash inside fence is not a heading",
		body: "## H\n\n```sh\n## not a heading\n```\n\n```output\nr\n```\n",
	},
	{
		name: "duplicate headings share a slug",
		body: "## Results\n\n```sh\na\n```\n\n## Results\n\n```sh\nb\n```\n",
	},
	{
		name: "non-ascii heading yields an empty slug",
		body: "## 日本語\n\n```sh\na\n```\n",
	},
	{
		name: "unclosed fence runs to end of file",
		body: "## H\n\n```sh\nno closing fence\n",
	},
	{
		name: "no trailing newline",
		body: "## H\n\n```sh\na\n```",
	},
	{
		name: "crlf line endings",
		body: "## H\r\n\r\n```sh\r\na\r\n```\r\n",
	},
	{
		name:             "fence inside list item",
		body:             "## H\n\n- item\n  ```sh\n  a\n  ```\n",
		goldmarkDiverges: true,
	},
	{
		name: "fence inside blockquote is not a cell",
		body: "## H\n\n> ```sh\n> a\n> ```\n",
	},
}

func mustParse(t *testing.T, src string) *Notebook {
	t.Helper()
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v\n--- source ---\n%s", err, src)
	}
	return n
}

// changed returns the span of before that differs from after, computed as the
// region between the common prefix and common suffix.
//
// Splice tests assert on this rather than on value equality, because equality can
// pass while a tool has rewritten neighbouring bytes and coincidentally reproduced
// them — "only the expected range changed" is the actual requirement (§11.5).
func changed(before, after []byte) (span Span, replacement string) {
	p := 0
	for p < len(before) && p < len(after) && before[p] == after[p] {
		p++
	}
	s := 0
	for s < len(before)-p && s < len(after)-p &&
		before[len(before)-1-s] == after[len(after)-1-s] {
		s++
	}
	return Span{Start: p, End: len(before) - s}, string(after[p : len(after)-s])
}
