package meta

import "testing"

// seeds cover the shapes the grammar admits plus a few it rejects, so the fuzzer
// starts from both sides of the boundary.
var seeds = []string{
	"",
	"sh",
	"sh {format=csv}",
	"output {truncated}",
	`output {format=csv, run="2026-07-16T09:41:07Z", tool="clinote/2.0"}`,
	`error {status=127, run="2026-07-16T09:44:12Z", tool="clinote/2.0"}`,
	"cypher {id=a7f3k2p9}",
	"sh {  a = 1 ,   b   }",
	`sh {msg="a \"b\" c\\d"}`,
	`sh {msg=""}`,
	"sh {a_b-c2=x}",
	"sh{a=1}",
	"sh {}",
	"sh {a=1",
	`sh {a="b}`,
	`sh {a="b\nc"}`,
	"sh {a=1, a=2}",
}

// FuzzMetaRoundTrip asserts the invariant that makes §10 hold: whatever Parse
// accepts, String returns byte-identically. Parse must never panic.
func FuzzMetaRoundTrip(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in, err := Parse(s)
		if err != nil {
			return // rejection is a valid outcome; it must simply not panic
		}
		if got := in.String(); got != s {
			t.Fatalf("round trip: Parse(%q).String() = %q", s, got)
		}
	})
}

// FuzzMetaInsertIsAdditive asserts the §9 append-only guarantee over arbitrary
// input: inserting an entry never removes a byte, never disturbs an existing
// entry, and always yields something Parse accepts.
func FuzzMetaInsertIsAdditive(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		before, err := Parse(s)
		if err != nil {
			return
		}
		out, err := before.Insert("id", "a7f3k2p9")
		if err != nil {
			return // refusing (untagged fence, key present) is valid
		}
		if len(out) <= len(s) {
			t.Fatalf("Insert shrank or kept length: %q -> %q", s, out)
		}
		after, err := Parse(out)
		if err != nil {
			t.Fatalf("Insert produced unparseable output %q: %v", out, err)
		}
		if after.Tag() != before.Tag() {
			t.Fatalf("Insert changed tag: %q -> %q", before.Tag(), after.Tag())
		}
		bEntries, aEntries := before.Entries(), after.Entries()
		if len(aEntries) != len(bEntries)+1 {
			t.Fatalf("entry count %d -> %d for %q", len(bEntries), len(aEntries), s)
		}
		for i, e := range bEntries {
			if aEntries[i] != e {
				t.Fatalf("Insert disturbed entry %d of %q: %#v -> %#v", i, s, e, aEntries[i])
			}
		}
		if e, ok := after.Get("id"); !ok || e.Value != "a7f3k2p9" {
			t.Fatalf("inserted entry missing from %q", out)
		}
	})
}

// FuzzMetaFormatParse asserts that canonical output is always re-readable and
// preserves every key, value, and flag — the property tool-written result blocks
// depend on.
func FuzzMetaFormatParse(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in, err := Parse(s)
		if err != nil {
			return
		}
		out, err := Format(in.Tag(), in.Entries(), []string{"format", "run", "tool"})
		if err != nil {
			return
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("Format output %q is unparseable: %v", out, err)
		}
		if len(again.Entries()) != len(in.Entries()) {
			t.Fatalf("entry count %d -> %d via canonical form %q", len(in.Entries()), len(again.Entries()), out)
		}
		for _, want := range in.Entries() {
			got, ok := again.Get(want.Key)
			if !ok {
				t.Fatalf("key %q lost in canonical form %q", want.Key, out)
			}
			if got.Value != want.Value || got.Flag != want.Flag {
				t.Fatalf("entry %q changed via canonical form: %#v -> %#v", want.Key, want, got)
			}
		}
		// Canonical form is a fixed point: formatting it again changes nothing.
		twice, err := Format(again.Tag(), again.Entries(), []string{"format", "run", "tool"})
		if err != nil {
			t.Fatalf("Format of canonical form failed: %v", err)
		}
		if twice != out {
			t.Fatalf("canonical form not idempotent: %q -> %q", out, twice)
		}
	})
}
