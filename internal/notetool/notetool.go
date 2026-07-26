// Package notetool holds the little that the notebook binaries have to agree on.
//
// Everything here was duplicated across cmd/clinote and cmd/sqlnote, and one copy had
// already drifted: sqlnote suggested clinote for a `bash`-tagged notebook, which clinote
// refuses too, because the hint was hand-maintained rather than derived from what the
// executors actually claim. A single [Tools] list is what stops that recurring.
//
// This is deliberately not part of the kit. The kit implements the format and the runtime;
// which binaries exist and what they are called is neither, and a kit that knew tool names
// would invert the dependency — tools are compiled in, so only the module can know its own
// set. internal/ is what keeps that knowledge out of the public API.
package notetool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pmuston/notekit/doc"
)

// Tool is a notebook binary and the info-string tag its executor runs.
type Tool struct {
	// Name is the binary's name, as a user would type it.
	Name string

	// Lang is the single tag the executor claims — the same value its Lang() returns.
	// Exactly one, and exact: package run compares tags for equality, so a tool that
	// claims "sh" does not run cells tagged "bash", and pretending otherwise would send
	// someone to a second tool that also refuses.
	Lang string
}

// Tools is the authoritative list of notebook binaries in this module.
//
// Add an entry when a new notebook tool lands, and its executor's Lang must match: the
// suggestion machinery has no other source of truth. Each tool asserts its own entry in
// TestToolsListsThisTool — the check has to live there, because a main package cannot be
// imported — so a mismatch fails that tool's tests rather than misdirecting a user.
var Tools = []Tool{
	{Name: "clinote", Lang: "sh"},
	{Name: "sqlnote", Lang: "sql"},
}

// Sibling names the tool that runs any of langs, skipping self. It returns "" when no
// tool in this module can — which is the honest answer for a tag nothing claims, and
// better than naming a tool that would refuse in turn.
func Sibling(langs []string, self string) string {
	for _, l := range langs {
		for _, t := range Tools {
			if t.Lang == l && t.Name != self {
				return t.Name
			}
		}
	}
	return ""
}

// CheckEngine reports why this binary cannot run the notebook at path, or nil.
//
// A notebook's engine is derived from the info-string tags its cells carry (format spec
// §2.1): nothing in front matter names a runner, so the tags are the only source of truth
// and there is nothing that can disagree with them.
//
// Two cases are deliberately allowed. A notebook with **no cells** has nothing to
// contradict — and a notebook made by [Create] always has one, so this is the half-written
// case rather than a broken one. A notebook where only **some** cells match is allowed
// because package run checks each cell as it runs it; refusing the whole file would be
// stricter than the format.
//
// The point of checking here at all is *when*: without it, a mismatch surfaced only when
// someone clicked Run, once per cell, in wording written for a developer — after the server
// had started and the page had rendered as though it were ready.
func CheckEngine(path, self, lang string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	langs := nb.Langs()
	if len(langs) == 0 {
		return nil
	}
	for _, l := range langs {
		if l == lang {
			return nil
		}
	}
	msg := fmt.Sprintf("%s has %s cells, and %s runs %q cells",
		path, quoteList(langs), self, lang)
	if sib := Sibling(langs, self); sib != "" {
		msg += "\n  try: " + sib + " " + path
	}
	return errors.New(msg)
}

// Create writes a new notebook at path: front matter, then first and nothing else.
//
// The cell is required rather than optional. [CheckEngine] derives a notebook's engine from
// its cells' tags, and a cell-less notebook is the one case that cannot answer — so a
// starter cell is what keeps every notebook's engine knowable from the moment it exists.
// Format spec §10 (g) governs the whole of this write.
//
// An existing file is never touched. The file is the artifact, so overwriting one on a
// mistyped path would destroy work no tool can recover, and refusing costs one command.
func Create(path string, first doc.NewCell) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := doc.Scaffold(TitleFromPath(path), first)
	if err != nil {
		return err
	}
	// The same atomic write every other durable write uses: a reader must never see a
	// half-written notebook.
	return doc.WriteFileAtomic(path, src, 0o644)
}

// TitleFromPath derives a readable title from a filename, so `new parts-list.md` opens as
// "Parts list" rather than "parts-list".
func TitleFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		return ""
	}
	// Decode the first rune rather than slicing the first byte: base[:1] would cut a
	// multi-byte character in half and emit a replacement character, so `ünïcode.md`
	// became a mojibake title. Both copies of this had the bug before it moved here.
	first, size := utf8.DecodeRuneInString(base)
	if first == utf8.RuneError {
		return base
	}
	return string(unicode.ToUpper(first)) + base[size:]
}

// quoteList renders a language list for an error message.
func quoteList(langs []string) string {
	quoted := make([]string, len(langs))
	for i, l := range langs {
		quoted[i] = fmt.Sprintf("%q", l)
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}
