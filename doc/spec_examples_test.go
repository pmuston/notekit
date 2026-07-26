package doc

import (
	"os"
	"strings"
	"testing"
)

// specFiles are the documents whose embedded examples must themselves conform.
var specFiles = []string{
	"../notekit-format-spec.md",
	"../notekit-rendering-contract.md",
	"../notekit-kit-spec.md",
}

// TestSpecExamplesConform is part of the M0 gate: every ````markdown example in the
// specs is extracted, wrapped in front matter, and parsed.
//
// This is not decoration. The spec's §8 example once separated provenance attributes
// with spaces while §9's grammar required commas — an example that contradicted the
// rule it illustrated, and exactly the kind of drift a reader copies into a tool. This
// test would have caught it: a malformed info string anywhere in an example fails here.
func TestSpecExamplesConform(t *testing.T) {
	total := 0
	for _, path := range specFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		examples := extractMarkdownExamples(string(src))
		for _, ex := range examples {
			total++
			t.Run(path+":"+itoa(ex.line), func(t *testing.T) {
				body := ex.body
				// Examples are fragments; front matter makes each a notebook.
				if !strings.HasPrefix(body, "---\n") {
					body = front + body
				}
				n, err := Parse([]byte(body))
				if err != nil {
					t.Fatalf("example does not parse: %v\n--- example ---\n%s", err, ex.body)
				}
				if string(n.Bytes()) != body {
					t.Errorf("example does not round-trip")
				}

				// Every info string in an example must be well-formed: a spec
				// example is the thing implementers copy.
				for _, c := range n.Cells() {
					if c.MetaErr != nil {
						t.Errorf("cell %q has a malformed info string: %v\n--- example ---\n%s",
							c.HeadingText, c.MetaErr, ex.body)
					}
					for _, r := range c.Results {
						if r.MetaErr != nil {
							t.Errorf("cell %q has malformed %s result metadata: %v\n--- example ---\n%s",
								c.HeadingText, r.Form, r.MetaErr, ex.body)
						}
					}
				}
			})
		}
	}
	if total == 0 {
		t.Fatal("no ````markdown examples found in the specs; the extractor is broken")
	}
	t.Logf("checked %d spec examples", total)
}

type specExample struct {
	line int    // 1-based line of the opening fence, for test names
	body string // the example's content
}

// extractMarkdownExamples pulls out blocks fenced with four backticks and tagged
// `markdown`, which is how the specs quote notebook content containing three-backtick
// fences.
func extractMarkdownExamples(src string) []specExample {
	var out []specExample
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "````markdown" {
			continue
		}
		start := i + 1
		for j := start; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "````" {
				body := strings.Join(lines[start:j], "\n")
				if body != "" {
					body += "\n"
				}
				out = append(out, specExample{line: i + 1, body: body})
				i = j
				break
			}
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
