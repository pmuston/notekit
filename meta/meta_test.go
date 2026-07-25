package meta

import (
	"errors"
	"testing"
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		tag     string
		entries []Entry
	}{
		{name: "empty", in: "", tag: ""},
		{name: "tag only", in: "sh", tag: "sh"},
		{name: "tag with trailing space", in: "sh  ", tag: "sh"},
		{name: "leading space", in: " sh", tag: "sh"},
		{
			name: "single bare",
			in:   "sh {format=csv}",
			tag:  "sh",
			entries: []Entry{
				{Key: "format", Value: "csv", raw: "format=csv"},
			},
		},
		{
			name: "flag",
			in:   "output {truncated}",
			tag:  "output",
			entries: []Entry{
				{Key: "truncated", Flag: true, raw: "truncated"},
			},
		},
		{
			name: "reserved output set",
			in:   `output {format=csv, run="2026-07-16T09:41:07Z", tool="clinote/2.0"}`,
			tag:  "output",
			entries: []Entry{
				{Key: "format", Value: "csv", raw: "format=csv"},
				{Key: "run", Value: "2026-07-16T09:41:07Z", Quoted: true, raw: `run="2026-07-16T09:41:07Z"`},
				{Key: "tool", Value: "clinote/2.0", Quoted: true, raw: `tool="clinote/2.0"`},
			},
		},
		{
			name: "error status is bare",
			in:   `error {status=127, run="2026-07-16T09:44:12Z"}`,
			tag:  "error",
			entries: []Entry{
				{Key: "status", Value: "127", raw: "status=127"},
				{Key: "run", Value: "2026-07-16T09:44:12Z", Quoted: true, raw: `run="2026-07-16T09:44:12Z"`},
			},
		},
		{
			name: "insignificant whitespace around entries commas and equals",
			in:   "sh {  a = 1 ,   b   }",
			tag:  "sh",
			entries: []Entry{
				{Key: "a", Value: "1", raw: "a = 1"},
				{Key: "b", Flag: true, raw: "b"},
			},
		},
		{
			name: "escapes in quoted value",
			in:   `sh {msg="a \"b\" c\\d"}`,
			tag:  "sh",
			entries: []Entry{
				{Key: "msg", Value: `a "b" c\d`, Quoted: true, raw: `msg="a \"b\" c\\d"`},
			},
		},
		{
			name: "empty quoted value",
			in:   `sh {msg=""}`,
			tag:  "sh",
			entries: []Entry{
				{Key: "msg", Value: "", Quoted: true, raw: `msg=""`},
			},
		},
		{
			name: "key charset",
			in:   "sh {a_b-c2=x}",
			tag:  "sh",
			entries: []Entry{
				{Key: "a_b-c2", Value: "x", raw: "a_b-c2=x"},
			},
		},
		{
			name: "id on a source fence",
			in:   "cypher {id=a7f3k2p9}",
			tag:  "cypher",
			entries: []Entry{
				{Key: "id", Value: "a7f3k2p9", raw: "id=a7f3k2p9"},
			},
		},
		{
			name: "trailing whitespace after closing brace",
			in:   "sh {a=1}  ",
			tag:  "sh",
			entries: []Entry{
				{Key: "a", Value: "1", raw: "a=1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) = error %v", tt.in, err)
			}
			if got.Tag() != tt.tag {
				t.Errorf("Tag() = %q, want %q", got.Tag(), tt.tag)
			}
			if len(got.Entries()) != len(tt.entries) {
				t.Fatalf("Entries() = %#v, want %#v", got.Entries(), tt.entries)
			}
			for i, want := range tt.entries {
				if got.Entries()[i] != want {
					t.Errorf("Entries()[%d] = %#v, want %#v", i, got.Entries()[i], want)
				}
			}
			// Round-trip identity: the original bytes come back untouched (§10).
			if got.String() != tt.in {
				t.Errorf("String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"brace touching tag", "sh{a=1}"},
		{"unterminated block", "sh {a=1"},
		{"unterminated block after comma", "sh {a=1,"},
		{"empty block", "sh {}"},
		{"empty block with space", "sh { }"},
		{"key starts with digit", "sh {1a=x}"},
		{"key starts with underscore", "sh {_a=x}"},
		{"upper-case key", "sh {A=x}"},
		{"missing separator", "sh {a=1 b=2}"},
		{"trailing content", "sh {a=1} extra"},
		{"not a brace after tag", "sh extra"},
		{"unterminated quote", `sh {a="b}`},
		{"unterminated escape", `sh {a="b\`},
		{"invalid escape", `sh {a="b\nc"}`},
		{"empty bare value", "sh {a=}"},
		{"empty bare value before comma", "sh {a=,b=1}"},
		{"stray closing brace in value", "sh {a=b}c}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.in); err == nil {
				t.Fatalf("Parse(%q) = nil error, want error", tt.in)
			}
		})
	}
}

func TestParseDuplicateKey(t *testing.T) {
	// §9 makes this a tool error rather than something to resolve silently.
	_, err := Parse("sh {a=1, b=2, a=3}")
	var dup *DuplicateKeyError
	if !errors.As(err, &dup) {
		t.Fatalf("Parse duplicate = %v, want *DuplicateKeyError", err)
	}
	if dup.Key != "a" {
		t.Errorf("Key = %q, want %q", dup.Key, "a")
	}
}

func TestInsert(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no metadata", "cypher", "cypher {id=a7f3k2p9}"},
		{"existing metadata", "cypher {format=csv}", "cypher {format=csv, id=a7f3k2p9}"},
		{"several entries", "sh {a=1, b}", "sh {a=1, b, id=a7f3k2p9}"},
		// §8.6: strictly additive — the space before '}' is preserved, and the
		// new entry still reads tidily because it lands before that space.
		{"trailing space inside braces", "sh {format=csv }", "sh {format=csv, id=a7f3k2p9 }"},
		{"untidy interior spacing preserved", "sh {  a = 1  }", "sh {  a = 1, id=a7f3k2p9  }"},
		{"trailing space after tag", "cypher  ", "cypher {id=a7f3k2p9}  "},
		{"trailing space after braces", "sh {a=1}  ", "sh {a=1, id=a7f3k2p9}  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) = error %v", tt.in, err)
			}
			got, err := in.Insert("id", "a7f3k2p9")
			if err != nil {
				t.Fatalf("Insert = error %v", err)
			}
			if got != tt.want {
				t.Errorf("Insert = %q, want %q", got, tt.want)
			}
			// The result must itself parse, and carry the new entry.
			re, err := Parse(got)
			if err != nil {
				t.Fatalf("Parse(inserted %q) = error %v", got, err)
			}
			e, ok := re.Get("id")
			if !ok || e.Value != "a7f3k2p9" {
				t.Errorf("Get(id) = %#v, %v", e, ok)
			}
		})
	}
}

// TestInsertPreservesExistingEntryBytes is the §9 append-only guarantee stated as
// a property: every pre-existing entry's raw text survives an insertion exactly.
func TestInsertPreservesExistingEntryBytes(t *testing.T) {
	const src = `sh {  format = csv ,run="a \"b\" c", flag  }`
	before, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse = error %v", err)
	}
	out, err := before.Insert("id", "zzzz2345")
	if err != nil {
		t.Fatalf("Insert = error %v", err)
	}
	after, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(inserted) = error %v", err)
	}
	if len(after.Entries()) != len(before.Entries())+1 {
		t.Fatalf("entry count = %d, want %d", len(after.Entries()), len(before.Entries())+1)
	}
	for i, e := range before.Entries() {
		if after.Entries()[i] != e {
			t.Errorf("entry %d changed: %#v -> %#v", i, e, after.Entries()[i])
		}
	}
}

func TestInsertErrors(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		in, err := Parse("sh {id=aaaa2345}")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := in.Insert("id", "bbbb2345"); err == nil {
			t.Fatal("Insert duplicate = nil error, want error")
		}
	})
	t.Run("invalid key", func(t *testing.T) {
		in, _ := Parse("sh")
		if _, err := in.Insert("Id", "x"); err == nil {
			t.Fatal("Insert invalid key = nil error, want error")
		}
	})
	t.Run("untagged fence", func(t *testing.T) {
		in, _ := Parse("")
		if _, err := in.Insert("id", "x"); err == nil {
			t.Fatal("Insert on untagged fence = nil error, want error")
		}
	})
}

func TestFormat(t *testing.T) {
	// Illustrative reserved orders; the real ones belong to the package that
	// writes result blocks (§6/§7), not to meta.
	outputOrder := []string{"format", "run", "tool"}

	tests := []struct {
		name     string
		tag      string
		entries  []Entry
		reserved []string
		want     string
	}{
		{name: "tag only", tag: "sh", want: "sh"},
		{
			name:    "single entry",
			tag:     "sh",
			entries: []Entry{{Key: "format", Value: "csv"}},
			want:    "sh {format=csv}",
		},
		{
			name: "reserved keys first, passthrough in original order",
			tag:  "output",
			entries: []Entry{
				{Key: "zeta", Value: "1"},
				{Key: "tool", Value: "clinote/2.0", Quoted: true},
				{Key: "alpha", Value: "2"},
				{Key: "format", Value: "csv"},
			},
			reserved: outputOrder,
			want:     `output {format=csv, tool="clinote/2.0", zeta=1, alpha=2}`,
		},
		{
			name:     "reserved key absent is skipped",
			tag:      "output",
			entries:  []Entry{{Key: "tool", Value: "x"}},
			reserved: outputOrder,
			want:     "output {tool=x}",
		},
		{
			name:    "flag entry",
			tag:     "output",
			entries: []Entry{{Key: "truncated", Flag: true}},
			want:    "output {truncated}",
		},
		{
			name:    "value quoted because it must be",
			tag:     "sh",
			entries: []Entry{{Key: "a", Value: "has space"}},
			want:    `sh {a="has space"}`,
		},
		{
			name:    "empty value is quoted",
			tag:     "sh",
			entries: []Entry{{Key: "a", Value: ""}},
			want:    `sh {a=""}`,
		},
		{
			name:    "escaping on write",
			tag:     "sh",
			entries: []Entry{{Key: "a", Value: `q"b\c`}},
			want:    `sh {a="q\"b\\c"}`,
		},
		{
			name:    "quoted honoured even when unnecessary",
			tag:     "output",
			entries: []Entry{{Key: "run", Value: "2026-07-16T09:41:07Z", Quoted: true}},
			want:    `output {run="2026-07-16T09:41:07Z"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Format(tt.tag, tt.entries, tt.reserved)
			if err != nil {
				t.Fatalf("Format = error %v", err)
			}
			if got != tt.want {
				t.Fatalf("Format = %q, want %q", got, tt.want)
			}
			// Anything Format writes must parse back to the same entry set.
			in, err := Parse(got)
			if err != nil {
				t.Fatalf("Parse(%q) = error %v", got, err)
			}
			if len(in.Entries()) != len(tt.entries) {
				t.Fatalf("re-parsed %d entries, want %d", len(in.Entries()), len(tt.entries))
			}
			for _, want := range tt.entries {
				e, ok := in.Get(want.Key)
				if !ok {
					t.Fatalf("Get(%q) missing after round trip", want.Key)
				}
				if e.Value != want.Value || e.Flag != want.Flag {
					t.Errorf("entry %q = %#v, want value %q flag %v", want.Key, e, want.Value, want.Flag)
				}
			}
		})
	}
}

func TestEntryRaw(t *testing.T) {
	in, err := Parse("sh {  format = csv  }")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := in.Get("format")
	// Raw is the entry's source text with surrounding whitespace excluded — the
	// boundary Insert appends at.
	if got, want := e.Raw(), "format = csv"; got != want {
		t.Errorf("Raw() = %q, want %q", got, want)
	}
	if got := (Entry{Key: "a", Value: "b"}).Raw(); got != "" {
		t.Errorf("Raw() on constructed Entry = %q, want empty", got)
	}
}

func TestErrorMessages(t *testing.T) {
	// Error text is user-facing: a tool refusing a fence should say where.
	syn := (&SyntaxError{Offset: 7, Msg: "expected ',' or '}'"}).Error()
	if want := "info string: expected ',' or '}' at offset 7"; syn != want {
		t.Errorf("SyntaxError = %q, want %q", syn, want)
	}
	dup := (&DuplicateKeyError{Key: "run", Offset: 12}).Error()
	if want := `info string: duplicate key "run" at offset 12`; dup != want {
		t.Errorf("DuplicateKeyError = %q, want %q", dup, want)
	}
}

func TestFormatErrors(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		entries []Entry
	}{
		{"duplicate key", "sh", []Entry{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}},
		{"invalid leading char in key", "sh", []Entry{{Key: "A", Value: "1"}}},
		{"invalid trailing char in key", "sh", []Entry{{Key: "a!b", Value: "1"}}},
		{"empty key", "sh", []Entry{{Key: "", Value: "1"}}},
		{"tag with space", "s h", nil},
		{"tag with brace", "s{h", nil},
		{"metadata without tag", "", []Entry{{Key: "a", Value: "1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Format(tt.tag, tt.entries, nil); err == nil {
				t.Fatalf("Format(%q, %#v) = nil error, want error", tt.tag, tt.entries)
			}
		})
	}
}
