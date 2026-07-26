package doc

import (
	"strings"
	"testing"
	"time"

	"github.com/pmuston/notekit/meta"
)

// TestFenceLen is §11.6: the fence must clear the longest backtick run in the body.
func TestFenceLen(t *testing.T) {
	tests := []struct {
		body string
		want int
	}{
		{"", 3},
		{"plain text\n", 3},
		{"`one`\n", 3},
		{"``two``\n", 3},
		{"```three```\n", 4},
		{"````four````\n", 5},
		{"`````five`````\n", 6},
		{"a ``` b ````` c\n", 6}, // longest run wins, not the first
		{"```\n```\n", 4},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			if got := FenceLen(tt.body); got != tt.want {
				t.Errorf("FenceLen(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

// TestResultBlockFenceSafety checks the property that matters rather than the count:
// whatever fence is chosen, the body cannot terminate it early, so the block
// re-parses with its body intact.
func TestResultBlockFenceSafety(t *testing.T) {
	bodies := []string{
		"plain\n",
		"```\ninner\n```\n",
		"````\ninner\n````\n",
		"`````\ninner\n`````\n",
		"trailing ```",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			text, err := ResultBlock{Form: ResultOutput, Body: body}.String()
			if err != nil {
				t.Fatalf("String: %v", err)
			}
			src := front + "## H\n\n```sh\na\n```\n\n" + text
			n := mustParse(t, src)
			c := n.Cells()[0]
			if len(c.Results) != 1 {
				t.Fatalf("got %d results, want 1 — the fence was terminated early", len(c.Results))
			}
			if c.Results[0].Form != ResultOutput {
				t.Errorf("form = %v, want output", c.Results[0].Form)
			}
			// The body must survive verbatim modulo newline normalisation.
			want := normaliseBody(body)
			gotBlock := string(c.Results[0].Span.In(n.Bytes()))
			if !strings.Contains(gotBlock, want) {
				t.Errorf("body not preserved:\n got %q\nwant to contain %q", gotBlock, want)
			}
		})
	}
}

func TestResultBlockCanonicalForm(t *testing.T) {
	run := time.Date(2026, 7, 16, 9, 41, 7, 0, time.UTC)
	status := 127

	tests := []struct {
		name  string
		block ResultBlock
		want  string
	}{
		{
			name:  "bare output",
			block: ResultBlock{Form: ResultOutput, Body: "hi\n"},
			want:  "```output\nhi\n```\n",
		},
		{
			// Absent and text mean the same thing, so the key is omitted (§6).
			name:  "text format is omitted",
			block: ResultBlock{Form: ResultOutput, Format: "text", Body: "hi\n"},
			want:  "```output\nhi\n```\n",
		},
		{
			name: "full output metadata in canonical order",
			block: ResultBlock{
				Form: ResultOutput, Format: "csv", Run: run,
				Tool: "clinote/2.0", Body: "size,path\n1.2G,./data\n",
			},
			want: "```output {format=csv, run=\"2026-07-16T09:41:07Z\", tool=\"clinote/2.0\"}\n" +
				"size,path\n1.2G,./data\n```\n",
		},
		{
			name: "truncated flag comes after the reserved keys",
			block: ResultBlock{
				Form: ResultOutput, Format: "csv", Truncated: true, Body: "partial\n",
			},
			want: "```output {format=csv, truncated}\npartial\n```\n",
		},
		{
			// §7's example puts status first, which is what ErrorKeyOrder encodes.
			name: "error with status",
			block: ResultBlock{
				Form: ResultError, Status: &status, Run: run,
				Tool: "clinote/2.0", Body: "zsh: command not found: dv\n",
			},
			want: "```error {status=127, run=\"2026-07-16T09:41:07Z\", tool=\"clinote/2.0\"}\n" +
				"zsh: command not found: dv\n```\n",
		},
		{
			name:  "error with no status",
			block: ResultBlock{Form: ResultError, Body: "boom\n"},
			want:  "```error\nboom\n```\n",
		},
		{
			name:  "empty body has no content lines",
			block: ResultBlock{Form: ResultOutput, Body: ""},
			want:  "```output\n```\n",
		},
		{
			name:  "trailing newlines normalise to one",
			block: ResultBlock{Form: ResultOutput, Body: "hi\n\n\n\n"},
			want:  "```output\nhi\n```\n",
		},
		{
			name:  "missing trailing newline gains one",
			block: ResultBlock{Form: ResultOutput, Body: "hi"},
			want:  "```output\nhi\n```\n",
		},
		{
			name: "passthrough keys follow the reserved ones",
			block: ResultBlock{
				Form: ResultOutput, Format: "csv", Body: "x\n",
				Extra: []meta.Entry{{Key: "zeta", Value: "1"}, {Key: "alpha", Value: "2"}},
			},
			want: "```output {format=csv, zeta=1, alpha=2}\nx\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.block.String()
			if err != nil {
				t.Fatalf("String: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResultBlockRejectsSidecarForm(t *testing.T) {
	if _, err := (ResultBlock{Form: ResultSidecar}).String(); err == nil {
		t.Fatal("want error: a sidecar is not an inline result block")
	}
}

// TestTruncation is §11.10's truncation marker, and pins the §8.4 ordering: the marker
// lands inside the body, so fence length is computed from the truncated text.
func TestTruncation(t *testing.T) {
	t.Run("under the cap is untouched", func(t *testing.T) {
		body := "short\n"
		got, did := Truncate(body, 100)
		if did || got != body {
			t.Errorf("Truncate = %q, %v; want unchanged", got, did)
		}
	})

	t.Run("marker appended", func(t *testing.T) {
		got, did := Truncate("aaaaaaaaaa\nbbbb\n", 11)
		if !did {
			t.Fatal("did = false, want true")
		}
		want := "aaaaaaaaaa\n" + TruncationLine(11) + "\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("newline inserted when the cut is mid-line", func(t *testing.T) {
		got, _ := Truncate("aaaaaaaaaaaaaaa", 5)
		want := "aaaaa\n" + TruncationLine(5) + "\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("cut backs off a rune boundary", func(t *testing.T) {
		// "日" is three bytes; cutting at 2 must not leave half a rune.
		got, _ := Truncate("日本語", 2)
		if strings.Contains(got, "�") {
			t.Errorf("got %q, which contains a replacement character", got)
		}
	})

	t.Run("§8.4 ordering: fence clears backticks in the truncated body", func(t *testing.T) {
		// Truncation cuts inside a five-backtick run, leaving three. The fence
		// must be computed from the *truncated* body, so four backticks.
		body := "x\n`````\n"
		cut, did := Truncate(body, 5)
		if !did {
			t.Fatal("expected truncation")
		}
		block, err := ResultBlock{Form: ResultOutput, Body: cut, Truncated: true}.String()
		if err != nil {
			t.Fatal(err)
		}
		src := front + "## H\n\n```sh\na\n```\n\n" + block
		n := mustParse(t, src)
		if got := len(n.Cells()[0].Results); got != 1 {
			t.Fatalf("got %d results, want 1 — the fence was terminated by the body", got)
		}
		if e, ok := n.Cells()[0].Results[0].Meta.Get("truncated"); !ok || !e.Flag {
			t.Errorf("truncated flag missing: %#v", e)
		}
	})
}

// TestStripANSI resolves and pins §8.5's scope.
func TestStripANSI(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello\n", "hello\n"},
		{"sgr colour", "\x1b[31mred\x1b[0m\n", "red\n"},
		{"sgr with several params", "\x1b[1;32;40mx\x1b[m\n", "x\n"},
		{"cursor movement", "a\x1b[2Ab\n", "ab\n"},
		{"erase line", "\x1b[2Kline\n", "line\n"},
		{"osc terminated by bel", "\x1b]0;title\x07text\n", "text\n"},
		{"osc terminated by st", "\x1b]0;title\x1b\\text\n", "text\n"},
		{"two-byte escape", "\x1b(Bplain\n", "plain\n"},
		{"lone escape at end", "text\x1b", "text"},
		{"crlf becomes lf", "a\r\nb\r\n", "a\nb\n"},
		{"progress bar carriage returns", "50%\r75%\r100%\n", "50%75%100%\n"},
		{"tabs kept", "a\tb\n", "a\tb\n"},
		{"nul and bell dropped", "a\x00b\x07c\n", "abc\n"},
		{"utf-8 preserved", "日本語\n", "日本語\n"},
		{"unterminated csi", "text\x1b[", "text"},
		{"unterminated osc", "text\x1b]0;", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripANSI(tt.in); got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResultBlockStripsANSI(t *testing.T) {
	got, err := ResultBlock{Form: ResultOutput, Body: "\x1b[31mred\x1b[0m\n"}.String()
	if err != nil {
		t.Fatal(err)
	}
	if want := "```output\nred\n```\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSidecarRef(t *testing.T) {
	run := time.Date(2026, 7, 16, 10, 2, 55, 0, time.UTC)

	tests := []struct {
		name string
		ref  SidecarRef
		want string
	}{
		{
			name: "full",
			ref: SidecarRef{
				Kind: "graph", Dest: "n.assets/wiring--k3m7q2vf.png",
				Alt: "Module wiring", Run: run, Tool: "graphtool/2.0",
			},
			want: "<!-- notekit:result kind=graph, run=\"2026-07-16T10:02:55Z\", tool=\"graphtool/2.0\" -->\n" +
				"![Module wiring](n.assets/wiring--k3m7q2vf.png)\n",
		},
		{
			name: "kind only",
			ref:  SidecarRef{Kind: "graph", Dest: "a.png", Alt: "A"},
			want: "<!-- notekit:result kind=graph -->\n![A](a.png)\n",
		},
		{
			name: "no attributes",
			ref:  SidecarRef{Dest: "a.png"},
			want: "<!-- notekit:result -->\n![](a.png)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ref.String()
			if err != nil {
				t.Fatalf("String: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			// Whatever is written must read back as one sidecar result.
			n := mustParse(t, front+"## H\n\n```cypher\na\n```\n\n"+got)
			c := n.Cells()[0]
			if len(c.Results) != 1 || c.Results[0].Form != ResultSidecar {
				t.Fatalf("results = %#v, want one sidecar", c.Results)
			}
			if c.Results[0].Dest != tt.ref.Dest {
				t.Errorf("Dest = %q, want %q", c.Results[0].Dest, tt.ref.Dest)
			}
			if c.Results[0].MetaErr != nil {
				t.Errorf("provenance metadata did not parse: %v", c.Results[0].MetaErr)
			}
		})
	}
}

func TestSidecarRefNeedsDest(t *testing.T) {
	if _, err := (SidecarRef{Kind: "graph"}).String(); err == nil {
		t.Fatal("want error for a reference with no destination")
	}
}

func TestSidecarName(t *testing.T) {
	tests := []struct{ slug, id, ext, want string }{
		{"disk-usage", "k3m7q2vf", "png", "disk-usage--k3m7q2vf.png"},
		{"disk-usage", "k3m7q2vf", ".png", "disk-usage--k3m7q2vf.png"},
		{"disk-usage", "k3m7q2vf", "json", "disk-usage--k3m7q2vf.json"},
		// An empty slug drops the separator entirely (§8).
		{"", "k3m7q2vf", "png", "k3m7q2vf.png"},
		{"", "k3m7q2vf", "", "k3m7q2vf"},
		{"a--b", "k3m7q2vf", "png", "a--b--k3m7q2vf.png"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := SidecarName(tt.slug, tt.id, tt.ext); got != tt.want {
				t.Errorf("SidecarName(%q, %q, %q) = %q, want %q", tt.slug, tt.id, tt.ext, got, tt.want)
			}
		})
	}
}

// TestSplitSidecarName pins the split-on-last-`--` rule, which is what lets a slug
// legitimately contain `--`.
func TestSplitSidecarName(t *testing.T) {
	tests := []struct {
		name string
		slug string
		id   string
		ok   bool
	}{
		{"disk-usage--k3m7q2vf.png", "disk-usage", "k3m7q2vf", true},
		{"a--b--k3m7q2vf.png", "a--b", "k3m7q2vf", true},
		{"k3m7q2vf.png", "", "k3m7q2vf", true},
		{"k3m7q2vf.json", "", "k3m7q2vf", true},
		{"no-id-here.png", "", "", false},
		{"slug--nothex.png", "", "", false},   // id must be valid base32
		{"slug--k3m7q2v9.png", "", "", false}, // 9 is not base32
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slug, id, ok := SplitSidecarName(tt.name)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (slug != tt.slug || id != tt.id) {
				t.Errorf("got slug=%q id=%q, want slug=%q id=%q", slug, id, tt.slug, tt.id)
			}
		})
	}
}

// TestSidecarNameRoundTrip is the property the naming scheme rests on: whatever
// SidecarName writes, SplitSidecarName recovers the id from.
func TestSidecarNameRoundTrip(t *testing.T) {
	slugs := []string{"", "a", "disk-usage", "a--b", strings.Repeat("x", SlugMaxLen)}
	ids := []string{"k3m7q2vf", "aaaaaaaa", "22222222"}
	for _, slug := range slugs {
		for _, id := range ids {
			name := SidecarName(slug, id, "png")
			gotSlug, gotID, ok := SplitSidecarName(name)
			if !ok {
				t.Errorf("SplitSidecarName(%q) failed", name)
				continue
			}
			if gotID != id || gotSlug != slug {
				t.Errorf("%q -> slug=%q id=%q, want slug=%q id=%q", name, gotSlug, gotID, slug, id)
			}
		}
	}
}

func TestSidecarDir(t *testing.T) {
	tests := []struct{ path, want string }{
		{"notes.md", "notes.assets"},
		{"./notes.md", "notes.assets"},
		{"/a/b/procsim-audit.md", "/a/b/procsim-audit.assets"},
		{"a/b/notes.markdown", "a/b/notes.assets"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := SidecarDir(tt.path); got != tt.want {
				t.Errorf("SidecarDir(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResultBlockRejectsBadExtraKeys(t *testing.T) {
	// Extra is passthrough but still has to satisfy the §9 key grammar, and
	// duplicate keys are a tool error.
	tests := []struct {
		name  string
		extra []meta.Entry
	}{
		{"invalid key", []meta.Entry{{Key: "Bad", Value: "1"}}},
		{"duplicate of a reserved key", []meta.Entry{{Key: "format", Value: "csv"}}},
		{"duplicate within extra", []meta.Entry{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := ResultBlock{Form: ResultOutput, Format: "csv", Body: "x\n", Extra: tt.extra}
			if _, err := block.String(); err == nil {
				t.Error("ResultBlock.String() = nil error, want error")
			}
			ref := SidecarRef{Kind: "graph", Dest: "a.png", Extra: tt.extra}
			if _, err := ref.String(); err == nil && tt.name != "duplicate of a reserved key" {
				t.Error("SidecarRef.String() = nil error, want error")
			}
		})
	}
}
