package kind

import (
	"strings"
	"testing"
)

func TestANSIToHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "hello\n", want: "hello\n"},
		{
			// Escaping is the renderer's job: the output is inserted unescaped.
			name: "html escaped",
			in:   `<script>alert("x")&</script>`,
			want: `&lt;script&gt;alert(&#34;x&#34;)&amp;&lt;/script&gt;`,
		},
		{
			name: "foreground colour",
			in:   "\x1b[31mred\x1b[0m",
			want: `<span class="ansi-fg-1">red</span>`,
		},
		{
			name: "bright foreground",
			in:   "\x1b[91mbright\x1b[0m",
			want: `<span class="ansi-fg-9">bright</span>`,
		},
		{
			name: "background colour",
			in:   "\x1b[42mgreen bg\x1b[0m",
			want: `<span class="ansi-bg-2">green bg</span>`,
		},
		{
			name: "bright background",
			in:   "\x1b[102mbright bg\x1b[0m",
			want: `<span class="ansi-bg-10">bright bg</span>`,
		},
		{
			name: "combined attributes in one sequence",
			in:   "\x1b[1;32mbold green\x1b[0m",
			want: `<span class="ansi-bold ansi-fg-2">bold green</span>`,
		},
		{
			name: "styles",
			in:   "\x1b[3mi\x1b[0m\x1b[4mu\x1b[0m\x1b[9ms\x1b[0m",
			want: `<span class="ansi-italic">i</span><span class="ansi-underline">u</span><span class="ansi-strike">s</span>`,
		},
		{
			// An empty parameter list is a full reset, per the standard.
			name: "bare reset closes the span",
			in:   "\x1b[31mred\x1b[m tail",
			want: `<span class="ansi-fg-1">red</span> tail`,
		},
		{
			name: "specific resets close the span",
			in:   "\x1b[1mbold\x1b[22m normal",
			want: `<span class="ansi-bold">bold</span> normal`,
		},
		{
			// An unterminated sequence must still produce balanced HTML, or the
			// surrounding page structure breaks.
			name: "unclosed colour is closed at the end",
			in:   "\x1b[31mred with no reset",
			want: `<span class="ansi-fg-1">red with no reset</span>`,
		},
		{
			name: "nested colours nest spans",
			in:   "\x1b[31ma\x1b[32mb\x1b[0mc",
			want: `<span class="ansi-fg-1">a<span class="ansi-fg-2">b</span></span>c`,
		},
		{
			// Non-SGR escapes have no meaning in a document, so they vanish rather
			// than appearing as text.
			name: "cursor movement dropped",
			in:   "a\x1b[2Ab",
			want: "ab",
		},
		{name: "erase line dropped", in: "\x1b[2Kline", want: "line"},
		{name: "osc with bel dropped", in: "\x1b]0;title\x07text", want: "text"},
		{name: "osc with st dropped", in: "\x1b]0;title\x1b\\text", want: "text"},
		{name: "charset selection dropped", in: "\x1b(Bplain", want: "plain"},
		{name: "lone escape at end", in: "text\x1b", want: "text"},
		{name: "unterminated csi", in: "text\x1b[", want: "text"},
		{name: "unterminated osc", in: "text\x1b]0;", want: "text"},
		{name: "carriage return dropped", in: "a\r\nb", want: "a\nb"},
		{name: "tab kept", in: "a\tb", want: "a\tb"},
		{name: "nul dropped", in: "a\x00b", want: "ab"},
		{name: "utf-8 preserved", in: "日本語", want: "日本語"},
		{
			// Unparseable parameters are ignored rather than guessed at.
			name: "empty parameter ignored",
			in:   "\x1b[31;mtext",
			want: `<span class="ansi-fg-1">text</span>`,
		},
		{
			// Not garbage: 'a' is 0x61, inside the CSI final-byte range, so the
			// sequence legitimately ends there and the rest is literal text.
			name: "csi final byte ends the sequence",
			in:   "\x1b[abcmtext",
			want: "bcmtext",
		},
		{
			name: "utf-8 inside a coloured span",
			in:   "\x1b[32m日本語\x1b[0m",
			want: `<span class="ansi-fg-2">日本語</span>`,
		},
		{
			// Already-stripped text passes through, which is what lets one renderer
			// serve both a fresh result and one read from a notebook.
			name: "no-op on stripped text",
			in:   "size,path\n1.2G,./data\n",
			want: "size,path\n1.2G,./data\n",
		},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ANSIToHTML(tt.in); got != tt.want {
				t.Errorf("ANSIToHTML(%q) =\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestANSIToHTMLAlwaysBalanced is the property that matters most: unbalanced spans would
// corrupt the page around the result, so no input may produce them.
func TestANSIToHTMLAlwaysBalanced(t *testing.T) {
	inputs := []string{
		"\x1b[31m", "\x1b[31m\x1b[32m\x1b[33m", "\x1b[0m\x1b[0m",
		"\x1b[1m\x1b[31mx", "\x1b[", "\x1b", "\x1b[31mx\x1b[0m\x1b[0m",
		"\x1b[999mx", "\x1b[31;999mx", "\x1b[31m\x1b[m\x1b[m",
	}
	for _, in := range inputs {
		got := ANSIToHTML(in)
		opens := strings.Count(got, "<span")
		closes := strings.Count(got, "</span>")
		if opens != closes {
			t.Errorf("ANSIToHTML(%q) = %q: %d opens, %d closes", in, got, opens, closes)
		}
	}
}

func TestSGRClasses(t *testing.T) {
	tests := []struct {
		params  string
		classes []string
		reset   bool
	}{
		{params: "", reset: true},
		{params: "0", reset: true},
		{params: "31", classes: []string{"ansi-fg-1"}},
		{params: "1;4", classes: []string{"ansi-bold", "ansi-underline"}},
		// A reset mid-sequence discards what preceded it.
		{params: "31;0", reset: true},
		{params: "31;0;32", classes: []string{"ansi-fg-2"}, reset: true},
		{params: "999"},
	}
	for _, tt := range tests {
		t.Run("params="+tt.params, func(t *testing.T) {
			classes, reset := sgrClasses(tt.params)
			if reset != tt.reset {
				t.Errorf("reset = %v, want %v", reset, tt.reset)
			}
			if len(classes) != len(tt.classes) {
				t.Fatalf("classes = %v, want %v", classes, tt.classes)
			}
			for i := range tt.classes {
				if classes[i] != tt.classes[i] {
					t.Errorf("classes[%d] = %q, want %q", i, classes[i], tt.classes[i])
				}
			}
		})
	}
}
