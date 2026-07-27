// Package notetool implements the obligations the format spec places on a notebook tool
// rather than on the format: creating a notebook (§10 g), refusing one this binary cannot
// run (§2.1), and finding the one to open.
//
// Every notebook tool needs these, and they are tedious to get right — so a tool that had to
// reimplement them would, and two hand-written copies in this repo had already drifted before
// they were unified.
//
// **The kit still names no tools.** [Tool] carries the calling binary's own name and tag, and
// its [Tool.Peers] are supplied by the caller. That distinction is the whole reason this
// package can be public: a registry of binaries baked into a library would invert the
// dependency, since executors are compiled in and only a module can know its own set.
//
// Peers may be empty, and for a tool in its own repository it usually is. Nothing is lost:
// [Tool.Suggest] then falls back to the notebook's own advisory [doc.FrontKeyTool] key, which
// exists precisely because a compiled-in list cannot name a tool from another module.
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

// Peer is another notebook binary that the calling tool knows about, and the tag its
// executor runs.
//
// Exactly one tag, and exact: package run compares tags for equality, so a tool that claims
// "sh" does not run cells tagged "bash". Recording otherwise would send someone to a second
// tool that also refuses, which is worse than saying nothing — and is a bug this repo shipped
// once, from a hand-maintained list that was not derived from what the executors claim.
type Peer struct {
	Name string
	Lang string
}

// Tool is the calling binary: its own name and tag, plus whatever siblings it knows of.
//
// Peers is optional. A tool built in its own repository generally knows of none, and that is
// a supported configuration rather than a degraded one — see the package comment.
type Tool struct {
	// Name is the binary's name, as a user would type it.
	Name string

	// Lang is the single info-string tag this tool's executor runs — the value its
	// Lang() method returns.
	Lang string

	// Peers are the other notebook binaries this build knows about. May be nil.
	Peers []Peer
}

// langOf returns the tag a named peer runs, and whether this tool knows of it at all. An
// unknown name is not an error — it is the case the advisory key exists to cover.
func (t Tool) langOf(name string) (string, bool) {
	for _, p := range t.Peers {
		if p.Name == name {
			return p.Lang, true
		}
	}
	return "", false
}

// sibling names the peer that runs any of langs, skipping this tool. It returns "" when no
// peer can — the honest answer for a tag nothing claims, and better than naming a tool that
// would refuse in turn.
func (t Tool) sibling(langs []string) string {
	for _, l := range langs {
		for _, p := range t.Peers {
			if p.Lang == l && p.Name != t.Name {
				return p.Name
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
func (t Tool) CheckEngine(path string) error {
	_, err := t.Inspect(path)
	return err
}

// Inspect reports whether this binary can run the notebook at path, and anything worth
// saying about it that is not a refusal.
//
// err means refuse: no cell carries a tag this executor runs. warn means proceed and say so
// — currently only a [doc.FrontKeyTool] value that contradicts the cells. The two are
// separate returns because the key must never cause a refusal (§2.1): it is advisory, and
// treating a stale value as authoritative is exactly the failure it is defined to avoid.
func (t Tool) Inspect(path string) (warn string, err error) {
	src, rerr := os.ReadFile(path)
	if rerr != nil {
		return "", rerr
	}
	nb, perr := doc.Parse(src)
	if perr != nil {
		return "", fmt.Errorf("%s: %w", path, perr)
	}
	langs := nb.Langs()
	if w := t.ToolKeyWarning(nb); w != "" {
		warn = path + ": " + w
	}

	if len(langs) == 0 {
		return warn, nil
	}
	for _, l := range langs {
		if l == t.Lang {
			return warn, nil
		}
	}
	msg := fmt.Sprintf("%s has %s cells, and %s runs %q cells",
		path, quoteList(langs), t.Name, t.Lang)
	if sug := t.Suggest(nb); sug != "" {
		msg += "\n  try: " + sug + " " + path
	}
	return warn, errors.New(msg)
}

// Suggest names the tool to point someone at, skipping self.
//
// The notebook's own [doc.FrontKeyTool] wins, because it is the only source that can name a
// tool this build has never heard of. [Sibling] is the fallback for the notebooks that carry
// no key — every notebook written before the key existed, and any written by hand.
func (t Tool) Suggest(nb *doc.Notebook) string {
	if claimed := nb.Front()[doc.FrontKeyTool]; claimed != "" && claimed != t.Name {
		return claimed
	}
	return t.sibling(nb.Langs())
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
func (t Tool) ToolKeyWarning(nb *doc.Notebook) string {
	claimed := nb.Front()[doc.FrontKeyTool]
	if claimed == "" {
		return ""
	}
	claimedLang, known := t.langOf(claimed)
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
func (t Tool) Create(path string, first doc.NewCell) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Write the advisory key: this is the one moment a tool knows for certain which tool a
	// notebook is for, so recording it costs nothing and spares the next reader a guess.
	src, err := doc.Scaffold(TitleFromPath(path),
		[]doc.FrontKey{{Key: doc.FrontKeyTool, Value: t.Name}}, first)
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
