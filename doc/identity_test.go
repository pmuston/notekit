package doc

import (
	"strings"
	"testing"
)

// The cases in this file are format spec §11.9. They are regression tests, not
// hypotheticals: the superseded slug-as-identity scheme (harvest D1) failed every one
// of them, silently attaching sidecar artifacts to the wrong cells.

// TestIdentitySurvivesHeadingRename is the case that motivated storing an id at all.
// Under the old scheme a rename orphaned the artifact; now the id matches, so the file
// is merely stale and gets renamed.
func TestIdentitySurvivesHeadingRename(t *testing.T) {
	before := front + "## Module wiring\n\n```cypher {id=k3m7q2vf}\nMATCH (n) RETURN n\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Module wiring](n.assets/module-wiring--k3m7q2vf.png)\n"
	// A typo fix in the heading, nothing else.
	after := strings.Replace(before, "## Module wiring", "## Module wiring diagram", 1)

	nb, na := mustParse(t, before), mustParse(t, after)
	cb, ca := nb.Cells()[0], na.Cells()[0]

	if cb.ID != ca.ID {
		t.Fatalf("id changed on rename: %q -> %q", cb.ID, ca.ID)
	}
	if cb.Slug == ca.Slug {
		t.Fatalf("slug should have changed, both are %q", cb.Slug)
	}

	// The existing file is stale, not orphaned, and the wanted name tracks the slug.
	existing := []string{"module-wiring--k3m7q2vf.png", "module-wiring--k3m7q2vf.json"}
	got := ClassifySidecars(existing, na.Cells())
	for _, s := range got {
		if s.State != SidecarStale {
			t.Errorf("%s: state = %v, want stale", s.Name, s.State)
		}
		if s.Cell != ca {
			t.Errorf("%s: not attributed to the renamed cell", s.Name)
		}
	}
	if got[0].Want != "module-wiring-diagram--k3m7q2vf.png" {
		t.Errorf("Want = %q", got[0].Want)
	}
	if got[1].Want != "module-wiring-diagram--k3m7q2vf.json" {
		t.Errorf("Want = %q", got[1].Want)
	}
}

// TestIdentitySurvivesReorder is the case that used to corrupt silently: two cells with
// the same heading, disambiguated by document order, swapping places.
func TestIdentitySurvivesReorder(t *testing.T) {
	first := "## Results\n\n```sh {id=aaaa2345}\necho one\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Results](n.assets/results--aaaa2345.png)\n"
	second := "## Results\n\n```sh {id=bbbb2345}\necho two\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Results](n.assets/results--bbbb2345.png)\n"

	before := mustParse(t, front+first+"\n"+second)
	after := mustParse(t, front+second+"\n"+first)

	// Both orderings must agree on which id owns which source and which artifact.
	beforeMap := idMap(before)
	afterMap := idMap(after)
	if len(beforeMap) != 2 || len(afterMap) != 2 {
		t.Fatalf("expected 2 identified cells, got %d and %d", len(beforeMap), len(afterMap))
	}
	for id, want := range beforeMap {
		if got := afterMap[id]; got != want {
			t.Errorf("id %s: source/dest changed on reorder:\n got %q\nwant %q", id, got, want)
		}
	}

	// And the artifacts remain current in both orderings — no rename, no orphan.
	names := []string{"results--aaaa2345.png", "results--bbbb2345.png"}
	for _, n := range []*Notebook{before, after} {
		for _, s := range ClassifySidecars(names, n.Cells()) {
			if s.State != SidecarCurrent {
				t.Errorf("%s: state = %v, want current", s.Name, s.State)
			}
		}
	}
}

// idMap keys each identified cell's source text and result destination by id.
func idMap(n *Notebook) map[string]string {
	out := map[string]string{}
	for _, c := range n.Cells() {
		if c.ID == "" {
			continue
		}
		dest := ""
		if len(c.Results) > 0 {
			dest = c.Results[0].Dest
		}
		out[c.ID] = c.SourceText() + "|" + dest
	}
	return out
}

// TestIdentitySurvivesDuplicateHeadingInsertion inserts a third cell with the same
// heading above two existing ones. Under a positional suffix scheme every subsequent
// suffix would shift and every artifact would reattach to the wrong cell.
func TestIdentitySurvivesDuplicateHeadingInsertion(t *testing.T) {
	existing := "## Results\n\n```sh {id=aaaa2345}\necho one\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Results](n.assets/results--aaaa2345.png)\n\n" +
		"## Results\n\n```sh {id=bbbb2345}\necho two\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Results](n.assets/results--bbbb2345.png)\n"
	inserted := "## Results\n\n```sh\necho zero\n```\n\n"

	before := mustParse(t, front+existing)
	after := mustParse(t, front+inserted+existing)

	if len(after.Cells()) != 3 {
		t.Fatalf("got %d cells, want 3", len(after.Cells()))
	}
	// The new cell has no id: nothing assigns one until it produces a sidecar.
	if got := after.Cells()[0].ID; got != "" {
		t.Errorf("inserted cell id = %q, want empty", got)
	}
	// All three share a slug, and none carries a suffix.
	for i, c := range after.Cells() {
		if c.Slug != "results" {
			t.Errorf("cell %d slug = %q, want %q with no suffix", i, c.Slug, "results")
		}
	}
	for id, want := range idMap(before) {
		if got := idMap(after)[id]; got != want {
			t.Errorf("id %s: attachment changed on insertion:\n got %q\nwant %q", id, got, want)
		}
	}

	names := []string{"results--aaaa2345.png", "results--bbbb2345.png"}
	for _, s := range ClassifySidecars(names, after.Cells()) {
		if s.State != SidecarCurrent {
			t.Errorf("%s: state = %v, want current", s.Name, s.State)
		}
	}
}

// TestOrphanReportedNotDeleted is the third §11.9 case: an orphan now means exactly one
// thing, the cell was deleted.
func TestOrphanReportedNotDeleted(t *testing.T) {
	// The notebook once had a cell with id bbbb2345; it has been removed.
	src := front + "## Results\n\n```sh {id=aaaa2345}\necho one\n```\n\n" +
		"<!-- notekit:result kind=graph -->\n![Results](n.assets/results--aaaa2345.png)\n"
	n := mustParse(t, src)

	names := []string{
		"results--aaaa2345.png",
		"results--bbbb2345.png", // the deleted cell's artifact
		"README.txt",            // not written by notekit
	}
	got := ClassifySidecars(names, n.Cells())
	if len(got) != 3 {
		t.Fatalf("got %d classifications, want 3", len(got))
	}
	if got[0].State != SidecarCurrent {
		t.Errorf("%s: state = %v, want current", got[0].Name, got[0].State)
	}
	if got[1].State != SidecarOrphan {
		t.Errorf("%s: state = %v, want orphan", got[1].Name, got[1].State)
	}
	if got[1].ID != "bbbb2345" {
		t.Errorf("orphan id = %q", got[1].ID)
	}
	if got[2].State != SidecarForeign {
		t.Errorf("%s: state = %v, want foreign", got[2].Name, got[2].State)
	}
	// ClassifySidecars is pure: it reports, and cannot delete anything.
	if len(names) != 3 {
		t.Error("input listing was mutated")
	}
}

// TestNoPositionalSuffixesAnywhere is a guard on the format as a whole: no artifact
// name, slug, or classification may contain a document-order suffix.
func TestNoPositionalSuffixesAnywhere(t *testing.T) {
	src := front +
		"## Results\n\n```sh\na\n```\n\n" +
		"## Results\n\n```sh\nb\n```\n\n" +
		"## Results\n\n```sh\nc\n```\n"
	n := mustParse(t, src)
	if len(n.Cells()) != 3 {
		t.Fatalf("got %d cells, want 3", len(n.Cells()))
	}
	for i, c := range n.Cells() {
		if c.Slug != "results" {
			t.Errorf("cell %d slug = %q — a positional suffix has reappeared", i, c.Slug)
		}
	}
}

func TestSidecarStateString(t *testing.T) {
	cases := map[SidecarState]string{
		SidecarCurrent:   "current",
		SidecarStale:     "stale",
		SidecarOrphan:    "orphan",
		SidecarForeign:   "foreign",
		SidecarState(99): "unknown",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("SidecarState(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestClassifySidecarsIgnoresCellsWithoutIDs(t *testing.T) {
	// A cell with no id owns no artifact, so a file cannot be attributed to it.
	n := mustParse(t, front+"## Results\n\n```sh\na\n```\n")
	got := ClassifySidecars([]string{"results--aaaa2345.png"}, n.Cells())
	if len(got) != 1 || got[0].State != SidecarOrphan {
		t.Errorf("got %#v, want one orphan", got)
	}
}
