package doc

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Version is the only format version this package implements (§2).
const Version = 1

// NotNotebookError reports a file that is not a notekit notebook: no front matter,
// or front matter without `notekit: 1`. §2 requires tools to refuse such a file
// rather than guess at its structure.
type NotNotebookError struct {
	Reason string
}

func (e *NotNotebookError) Error() string {
	return "not a notekit notebook: " + e.Reason
}

// frontMatter is the parsed result of the leading `---` block.
//
// Only the two reserved scalars are interpreted. The rest of the block is kept as
// an opaque byte range: every other key is passthrough, preserved byte-for-byte
// (§2), and running it through a YAML marshaller is exactly how that guarantee
// would be lost.
type frontMatter struct {
	span    Span // including both --- delimiter lines
	version int
	title   string
	// scalars holds every indent-zero `key: value` pair, including the reserved
	// ones. §2 requires passthrough keys to be "exposed to the runtime
	// uninterpreted", and a tool that needs one — a database notebook naming its
	// database — has no other way to reach it.
	scalars map[string]string
}

// parseFrontMatter splits the leading front-matter block and reads its two
// reserved scalars. bodyStart is the offset at which CommonMark content begins.
func parseFrontMatter(src []byte) (fm frontMatter, bodyStart int, err error) {
	if !hasDelimiter(src, 0) {
		return fm, 0, &NotNotebookError{Reason: "missing YAML front matter"}
	}
	open := lineEnd(src, 0)

	// Find the closing delimiter.
	p := open
	closeStart := -1
	for p < len(src) {
		e := lineEnd(src, p)
		if hasDelimiter(src, p) {
			closeStart = p
			p = e
			break
		}
		p = e
	}
	if closeStart < 0 {
		return fm, 0, &NotNotebookError{Reason: "unterminated front matter"}
	}

	fm.span = Span{Start: 0, End: p}
	body := src[open:closeStart]

	scalars, err := frontMatterScalars(body)
	if err != nil {
		return fm, 0, err
	}
	version, found, title := 0, false, ""
	if v, ok := scalars["notekit"]; ok {
		n, convErr := strconv.Atoi(v)
		if convErr != nil {
			return fm, 0, &NotNotebookError{
				Reason: fmt.Sprintf("`notekit` must be an integer, got %q", v),
			}
		}
		version, found = n, true
	}
	title = unquoteScalar(scalars["title"])
	if !found {
		return fm, 0, &NotNotebookError{Reason: "front matter has no `notekit` key"}
	}
	if version != Version {
		return fm, 0, &NotNotebookError{
			Reason: fmt.Sprintf("format version %d is not supported (this tool implements %d)", version, Version),
		}
	}
	fm.version, fm.title, fm.scalars = version, title, scalars
	return fm, p, nil
}

// hasDelimiter reports whether the line at p is a `---` front-matter delimiter.
func hasDelimiter(src []byte, p int) bool {
	if p >= len(src) {
		return false
	}
	return bytes.Equal(lineText(src, p, lineEnd(src, p)), []byte("---"))
}

// frontMatterScalars reads every indent-zero `key: value` pair.
//
// This is deliberately not a YAML parser: it reads scalars at indent zero and ignores
// everything else, including nested structures, so no YAML dependency is needed and no
// passthrough key is ever reserialised. A key whose value is a nested block is reported
// with an empty value — enough to know it is there, not enough to misread it.
func frontMatterScalars(body []byte) (map[string]string, error) {
	out := make(map[string]string)
	for p := 0; p < len(body); {
		e := lineEnd(body, p)
		line := lineText(body, p, e)
		p = e

		// Only indent-zero lines are top-level keys; anything nested belongs to
		// a passthrough structure we do not interpret.
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || line[0] == '-' {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := string(bytes.TrimSpace(line[:colon]))
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(string(line[colon+1:]))
	}
	return out, nil
}

// unquoteScalar strips one layer of matching YAML quotes. Titles are displayed,
// never rewritten, so full YAML escape handling would buy nothing.
func unquoteScalar(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
