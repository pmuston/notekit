// Package siblings lists the notebook binaries this module ships.
//
// It is internal because it is the one piece of this that genuinely is repo-specific: which
// binaries exist here, and what tag each runs. [notetool] takes the list as a parameter
// precisely so it can stay out of the kit's public API, and this is where the answer for
// *this* module lives.
//
// One list rather than one per tool, because the alternative drifted: sqlnote once suggested
// clinote for a `bash`-tagged notebook, which clinote refuses too, sending the user to a
// second refusal. A tool cannot check another tool's claim about itself — a main package is
// not importable — so the guard is that each tool asserts its **own** entry here against its
// own Lang, in TestToolsListsThisTool. Between them the whole list is verified.
package siblings

import "github.com/pmuston/notekit/notetool"

// All is every notebook binary in this module, including the caller: [notetool.Tool.Suggest]
// skips the tool doing the asking, so a list containing self is correct and simpler than
// maintaining a different view per tool.
//
// Add an entry when a new notebook tool lands here, and add the matching assertion to its
// tests. A tool in its own repository needs no entry and no list at all.
var All = []notetool.Peer{
	{Name: "clinote", Lang: "sh"},
	{Name: "sqlnote", Lang: "sql"},
}
