// Package notetool holds the little that the notebook binaries have to agree on.
//
// Everything here was duplicated across cmd/clinote and cmd/sqlnote, and one copy had
// already drifted: sqlnote suggested clinote for a `bash`-tagged notebook, which clinote
// refuses too, because the hint was hand-maintained rather than derived from what the
// executors actually claim. A single [Tools] list is what stops that recurring.
//
// [Tools] is a compiled-in list, so it can only ever name tools in this module — and
// notebook tools live in their own repositories (clinote v1 and priortool already do). That
// is what the advisory [doc.FrontKeyTool] key is for: a notebook can name a tool nothing
// here has heard of. The two are complementary rather than redundant. The key says what the
// *file* claims; [Tools] says what this *build* knows, which is the only one of the two that
// can be checked. So the key supplies the suggestion and [Tools] audits it.
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
	"sort"
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

// LangOf returns the tag the named tool runs, and whether it is known here at all. A tool
// from another module is unknown, which is not an error — it is the case the advisory key
// exists to cover.
func LangOf(name string) (string, bool) {
	for _, t := range Tools {
		if t.Name == name {
			return t.Lang, true
		}
	}
	return "", false
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
	_, err := Inspect(path, self, lang)
	return err
}

// Inspect reports whether this binary can run the notebook at path, and anything worth
// saying about it that is not a refusal.
//
// err means refuse: no cell carries a tag this executor runs. warn means proceed and say so
// — currently only a [doc.FrontKeyTool] value that contradicts the cells. The two are
// separate returns because the key must never cause a refusal (§2.1): it is advisory, and
// treating a stale value as authoritative is exactly the failure it is defined to avoid.
func Inspect(path, self, lang string) (warn string, err error) {
	src, rerr := os.ReadFile(path)
	if rerr != nil {
		return "", rerr
	}
	nb, perr := doc.Parse(src)
	if perr != nil {
		return "", fmt.Errorf("%s: %w", path, perr)
	}
	langs := nb.Langs()
	if w := ToolKeyWarning(nb); w != "" {
		warn = path + ": " + w
	}

	if len(langs) == 0 {
		return warn, nil
	}
	for _, l := range langs {
		if l == lang {
			return warn, nil
		}
	}
	msg := fmt.Sprintf("%s has %s cells, and %s runs %q cells",
		path, quoteList(langs), self, lang)
	if sug := Suggest(nb, self); sug != "" {
		msg += "\n  try: " + sug + " " + path
	}
	return warn, errors.New(msg)
}

// Suggest names the tool to point someone at, skipping self.
//
// The notebook's own [doc.FrontKeyTool] wins, because it is the only source that can name a
// tool this build has never heard of. [Sibling] is the fallback for the notebooks that carry
// no key — every notebook written before the key existed, and any written by hand.
func Suggest(nb *doc.Notebook, self string) string {
	if claimed := nb.Front()[doc.FrontKeyTool]; claimed != "" && claimed != self {
		return claimed
	}
	return Sibling(nb.Langs(), self)
}

// ToolKeyWarning reports a [doc.FrontKeyTool] value that contradicts the notebook's cells,
// or "" when there is nothing to say. The message names no file; the caller places it.
//
// Only a *known* tool can be contradicted: if the key names something from another module
// there is no lang to compare against, and silence is correct — that is the case the key
// exists for. A notebook with no cells cannot contradict anything either.
//
// This is a warning and never more. The cells decide what runs; the key is hand-editable
// text, and a stale value is expected rather than exceptional.
func ToolKeyWarning(nb *doc.Notebook) string {
	claimed := nb.Front()[doc.FrontKeyTool]
	if claimed == "" {
		return ""
	}
	claimedLang, known := LangOf(claimed)
	if !known {
		return ""
	}
	langs := nb.Langs()
	if len(langs) == 0 {
		return ""
	}
	for _, l := range langs {
		if l == claimedLang {
			return ""
		}
	}
	// Path-free: notefmt already prefixes its findings with file:line, and repeating the
	// name there read as a stutter. Callers that need it add it themselves.
	return fmt.Sprintf("%s says %s, but the cells are %s — %s runs %q. "+
		"The cells decide; fix the key or ignore it",
		doc.FrontKeyTool, claimed, quoteList(langs), claimed, claimedLang)
}

// Create writes a new notebook at path: front matter naming self as the tool, then first
// and nothing else.
//
// The cell is required rather than optional. [CheckEngine] derives a notebook's engine from
// its cells' tags, and a cell-less notebook is the one case that cannot answer — so a
// starter cell is what keeps every notebook's engine knowable from the moment it exists.
// Format spec §10 (g) governs the whole of this write.
//
// An existing file is never touched. The file is the artifact, so overwriting one on a
// mistyped path would destroy work no tool can recover, and refusing costs one command.
func Create(path, self string, first doc.NewCell) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Write the advisory key: this is the one moment a tool knows for certain which tool a
	// notebook is for, so recording it costs nothing and spares the next reader a guess.
	src, err := doc.Scaffold(TitleFromPath(path),
		[]doc.FrontKey{{Key: doc.FrontKeyTool, Value: self}}, first)
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

// FindNotebooks lists the notekit notebooks in a directory, in name order.
//
// A file is a candidate only if it actually parses. Refusing to guess is the format's
// posture (§2), and offering a file that turns out not to be a notebook would only move the
// error somewhere worse — to the moment the user picked it.
func FindNotebooks(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if parseable(p) != nil {
			continue
		}
		found = append(found, p)
	}
	sort.Strings(found)
	return found, nil
}

// Resolve returns the notebook to open: arg when given, otherwise the one candidate in dir.
//
// The picker is deliberately not interactive. With exactly one candidate the answer is
// obvious, and with several the useful thing is to name them and let the user choose rather
// than guess and open the wrong notebook.
//
// dir is a parameter rather than always ".", so this can be tested without chdir — a
// process-global that no test should have to reach for, and that stops tests running in
// parallel. Callers pass ".".
func Resolve(arg, dir string) (string, error) {
	if arg != "" {
		// A named file that is not a notebook is refused with the reason, rather than
		// silently falling back to the picker and opening something else.
		if err := parseable(arg); err != nil {
			return "", err
		}
		return arg, nil
	}

	found, err := FindNotebooks(dir)
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no notekit notebooks in %s; "+
			"name one, or create a file with `notekit: 1` front matter", describeDir(dir))
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("several notebooks here — name one of:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// describeDir names a directory the way a message should read.
func describeDir(dir string) string {
	if dir == "." || dir == "" {
		return "the current directory"
	}
	return dir
}

// parseable reports why a path is not a notebook this kit will open, in terms the user can
// act on, or nil when it is one.
func parseable(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := doc.Parse(src); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
