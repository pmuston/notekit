package kind

import (
	"strings"
	"testing"
)

func TestNewRegistryHoldsCoreKinds(t *testing.T) {
	r := NewRegistry()
	// The rendering contract's §2 core set, which every conforming tool must support.
	want := []string{Table, Text}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, name := range want {
		k, ok := r.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed", name)
		}
		if k.Durable == nil {
			t.Errorf("%q has no durable writer", name)
		}
		// Both core kinds render live as of M2.
		if k.Live == nil {
			t.Errorf("%q has no live renderer", name)
		}
		if len(k.Formats) == 0 {
			t.Errorf("%q claims no durable format values, so a persisted result "+
				"could not be routed back to it", name)
		}
	}
}

func TestLookupFormat(t *testing.T) {
	r := NewRegistry()
	tests := []struct {
		format string
		want   string
		ok     bool
	}{
		// Absent and "text" mean the same thing (§6), so text claims both.
		{format: "", want: Text, ok: true},
		{format: Text, want: Text, ok: true},
		{format: CSV, want: Table, ok: true},
		{format: JSONL, want: Table, ok: true},
		{format: "graph", ok: false},
		{format: "tsv", ok: false},
	}
	for _, tt := range tests {
		t.Run("format="+tt.format, func(t *testing.T) {
			k, ok := r.LookupFormat(tt.format)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && k.Name != tt.want {
				t.Errorf("kind = %q, want %q", k.Name, tt.want)
			}
		})
	}
}

// TestReplacingAKindReleasesItsFormats: a replacement that claims fewer formats must not
// leave the old claims dangling, or a persisted result would route to a kind that no
// longer handles it.
func TestReplacingAKindReleasesItsFormats(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.LookupFormat(JSONL); !ok {
		t.Fatal("jsonl should be claimed initially")
	}
	err := r.Register(Kind{Name: Table, Formats: []string{CSV}, Durable: durableTable})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.LookupFormat(JSONL); ok {
		t.Error("jsonl is still claimed after a replacement that dropped it")
	}
	if _, ok := r.LookupFormat(CSV); !ok {
		t.Error("csv should still be claimed")
	}
}

func TestRegisterRejectsInadmissibleKinds(t *testing.T) {
	r := NewEmptyRegistry()

	if err := r.Register(Kind{Name: "", Durable: durableText}); err == nil {
		t.Error("registering a nameless kind = nil error, want error")
	}
	// The two-forms rule: a kind with no durable form is inadmissible.
	if err := r.Register(Kind{Name: "graph"}); err == nil {
		t.Error("registering a kind with no durable writer = nil error, want error")
	}
	if len(r.Names()) != 0 {
		t.Errorf("Names() = %v, want empty", r.Names())
	}
}

func TestRegisterReplaces(t *testing.T) {
	r := NewRegistry()
	marker := func(any) (Durable, error) { return Durable{Inline: &Inline{Body: "replaced"}}, nil }
	if err := r.Register(Kind{Name: Text, Durable: marker}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k, _ := r.Lookup(Text)
	d, err := k.Durable(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Inline.Body != "replaced" {
		t.Errorf("replacement did not take effect: %q", d.Inline.Body)
	}
	if len(r.Names()) != 2 {
		t.Errorf("replacing changed the kind count: %v", r.Names())
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := NewRegistry().Lookup("nosuch"); ok {
		t.Error("Lookup of an unregistered kind returned ok")
	}
}

// TestRegistriesAreIndependent: a registry is a value, not a global, so a tool and a
// test cannot interfere with each other.
func TestRegistriesAreIndependent(t *testing.T) {
	a, b := NewRegistry(), NewRegistry()
	if err := a.Register(Kind{Name: "graph", Durable: durableText}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Lookup("graph"); ok {
		t.Error("registering in one registry leaked into another")
	}
}

func TestDurableText(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
		wantErr bool
	}{
		{name: "string", payload: "hello\n", want: "hello\n"},
		{name: "bytes", payload: []byte("hello\n"), want: "hello\n"},
		{name: "empty string", payload: "", want: ""},
		{name: "wrong type", payload: 42, wantErr: true},
		{name: "nil", payload: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := durableText(tt.payload)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("durableText: %v", err)
			}
			if d.Inline == nil {
				t.Fatal("Inline = nil, want the text form")
			}
			// Absent and "text" mean the same thing, so the key is omitted (§6).
			if d.Inline.Format != "" {
				t.Errorf("Format = %q, want empty", d.Inline.Format)
			}
			if d.Inline.Body != tt.want {
				t.Errorf("Body = %q, want %q", d.Inline.Body, tt.want)
			}
		})
	}
}

func TestDurableTable(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		format  string
		wantErr string
	}{
		{name: "csv", payload: TablePayload{Format: CSV, Body: "a,b\n1,2\n"}, format: CSV},
		{name: "jsonl", payload: TablePayload{Format: JSONL, Body: "{\"a\":1}\n"}, format: JSONL},
		{name: "pointer payload", payload: &TablePayload{Format: CSV, Body: "a\n"}, format: CSV},
		// The kit does not transcode between csv and jsonl, so an unknown
		// serialisation is an error rather than a guess.
		{name: "unknown format", payload: TablePayload{Format: "tsv"}, wantErr: "is not"},
		{name: "missing format", payload: TablePayload{Body: "a\n"}, wantErr: "is not"},
		{name: "wrong type", payload: "a,b\n", wantErr: "want kind.TablePayload"},
		{name: "nil pointer", payload: (*TablePayload)(nil), wantErr: "want kind.TablePayload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := durableTable(tt.payload)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("durableTable: %v", err)
			}
			if d.Inline == nil || d.Inline.Format != tt.format {
				t.Errorf("Inline = %#v, want format %q", d.Inline, tt.format)
			}
		})
	}
}

func TestDurableValidate(t *testing.T) {
	tests := []struct {
		name    string
		d       Durable
		wantErr string
	}{
		{name: "inline", d: Durable{Inline: &Inline{Body: "x"}}},
		{
			name: "sidecar with one primary",
			d: Durable{Sidecar: &Sidecar{Files: []File{
				{Ext: "png", Primary: true}, {Ext: "json"},
			}}},
		},
		{name: "neither", wantErr: "neither inline nor sidecar"},
		{
			name:    "both",
			d:       Durable{Inline: &Inline{}, Sidecar: &Sidecar{}},
			wantErr: "both inline and sidecar",
		},
		{name: "no files", d: Durable{Sidecar: &Sidecar{}}, wantErr: "no files"},
		{
			name:    "no primary",
			d:       Durable{Sidecar: &Sidecar{Files: []File{{Ext: "png"}}}},
			wantErr: "0 primary files",
		},
		{
			name: "two primaries",
			d: Durable{Sidecar: &Sidecar{Files: []File{
				{Ext: "png", Primary: true}, {Ext: "svg", Primary: true},
			}}},
			wantErr: "2 primary files",
		},
		{
			name:    "file with no extension",
			d:       Durable{Sidecar: &Sidecar{Files: []File{{Primary: true}}}},
			wantErr: "no extension",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.d.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
