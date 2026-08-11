// Package kind implements the result-kind registry.
//
// Implements notekit-rendering-contract.md. Every kind registers two halves, per the
// contract's two-forms rule: a durable writer (what persists, as plain CommonMark — an
// inline fence or a sidecar artifact plus reference) and a live renderer (what the
// browser shows during a session). A kind with no durable form is inadmissible, which
// is why [Kind.Durable] is required and [Kind.Live] is not.
//
// The two halves land at different milestones: package run needs the durable half from
// M1 in order to write a result block at all, while package serve uses the live half
// from M2. This is a correction to the kit spec's milestone list, which implies the
// whole registry arrives with serve.
//
// Core kinds, fixed for v1 and required of every tool: [Text], [Table] (durable as csv
// or jsonl), and error. Error is not a registered kind — a domain failure is an
// [exec.Error] and package run writes it as an `error` fence directly, because the
// format gives it a fixed form that no kind may vary.
//
// Registration is compile-time in the tool binary; there is no plugin loading. A
// registry is a value, not a global, so tests and tools each hold their own.
//
// Anything that exists only live — colour, sortability, interactivity — must degrade to
// nothing. The live view may be prettier than the durable form, never fuller.
package kind

import (
	"fmt"
	"sort"
)

// Core kind names, fixed for v1.
const (
	Text  = "text"
	Table = "table"
)

// Table serialisations (the `format` metadata value, format spec §6).
const (
	CSV   = "csv"
	JSONL = "jsonl"
)

// Kind is one registered result kind.
type Kind struct {
	// Name is the kind's identifier, lower-case and domain-prefixed for non-core
	// kinds. It is what an executor puts in [exec.Result.Kind].
	Name string

	// Formats lists the durable `format` values this kind renders live. It is what
	// lets a server go from a persisted result back to a renderer, since a notebook
	// on disk carries a `format`, not a kind name.
	//
	// The empty string is a legitimate entry, and is how `text` claims a fence with
	// no `format` key — absent and "text" mean the same thing (§6). A sidecar-only
	// kind lists nothing.
	Formats []string

	// Durable renders a payload into its durable form. Required: a kind without one
	// is inadmissible under the two-forms rule.
	Durable DurableFunc

	// Live renders a result for the browser. A nil Live means the kind persists
	// correctly but has no richer live view than its durable form, which is always a
	// legitimate position.
	Live LiveFunc
}

// DurableFunc turns an executor's payload into what persists.
type DurableFunc func(payload any) (Durable, error)

// Durable is a kind's durable form: exactly one of Inline or Sidecar.
//
// The runtime fills in everything the format owns — provenance metadata, fence length,
// truncation, sidecar filenames — so a kind states only what it knows: the
// serialisation and the bytes.
type Durable struct {
	Inline  *Inline
	Sidecar *Sidecar
}

// Inline is a durable form that lives in the notebook as an `output` fence (§6).
type Inline struct {
	// Format is the `format` metadata value: empty or "text" for plain text, "csv"
	// or "jsonl" for a table, or a domain value for an extension kind whose body is
	// a plain-text serialisation.
	Format string

	// Body is the result text. The runtime strips ANSI, normalises the trailing
	// newline, applies the output cap, and computes fence length.
	Body string
}

// Sidecar is a durable form that lives beside the notebook as an artifact plus a
// reference (§8). Required for anything binary or visual.
//
// The runtime names each file `<slug>--<id>.<ext>`, assigning the cell an id if it has
// none, and writes the reference into the cell's result position.
type Sidecar struct {
	// Kind is the value written as the provenance comment's `kind` attribute. It
	// defaults to the registered kind's Name when empty.
	Kind string

	// Alt is the image link's alt text. The cell's heading reads well here.
	Alt string

	// Files are the artifacts. Exactly one must be Primary — the one the image link
	// points at. The rendering contract requires the full data payload alongside the
	// rendered figure, so that a re-render needs no live engine.
	Files []File
}

// File is one sidecar artifact.
type File struct {
	Ext     string // extension without the dot, e.g. "png", "json"
	Content []byte
	Primary bool // the artifact the image link points at
}

// Validate checks the two-forms rule and the sidecar invariants.
func (d Durable) Validate() error {
	switch {
	case d.Inline == nil && d.Sidecar == nil:
		return fmt.Errorf("kind: durable form is neither inline nor sidecar")
	case d.Inline != nil && d.Sidecar != nil:
		return fmt.Errorf("kind: durable form is both inline and sidecar")
	}
	if d.Sidecar == nil {
		return nil
	}
	if len(d.Sidecar.Files) == 0 {
		return fmt.Errorf("kind: sidecar form has no files")
	}
	primaries := 0
	for _, f := range d.Sidecar.Files {
		if f.Ext == "" {
			return fmt.Errorf("kind: sidecar file has no extension")
		}
		if f.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return fmt.Errorf("kind: sidecar form has %d primary files, want exactly 1", primaries)
	}
	return nil
}

// Registry holds the kinds a tool supports.
//
// It is a value rather than a package global so that a test, a tool, and a second tool
// in the same binary cannot interfere with one another.
type Registry struct {
	kinds    map[string]Kind
	byFormat map[string]string // durable `format` value -> kind name
}

// NewRegistry returns a registry holding the core kinds (§2 of the rendering contract),
// which every conforming tool must support.
func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	// Registration cannot fail for these: the names are distinct and non-empty.
	_ = r.Register(Kind{
		Name:    Text,
		Formats: []string{"", Text},
		Durable: durableText,
		Live:    liveText,
	})
	_ = r.Register(Kind{
		Name:    Table,
		Formats: []string{CSV, JSONL},
		Durable: durableTable,
		Live:    liveTable,
	})
	return r
}

// NewEmptyRegistry returns a registry with no kinds, for tests that want to assert
// what happens when a kind is missing.
func NewEmptyRegistry() *Registry {
	return &Registry{
		kinds:    make(map[string]Kind),
		byFormat: make(map[string]string),
	}
}

// Register adds a kind, replacing any kind of the same name.
//
// Replacing is permitted so a tool can specialise a core kind — a shell notebook might
// give `text` a domain-specific live renderer — but the durable form it produces must
// still satisfy the format, which the conformance corpus is what checks.
func (r *Registry) Register(k Kind) error {
	if k.Name == "" {
		return fmt.Errorf("kind: cannot register a kind with no name")
	}
	if k.Durable == nil {
		return fmt.Errorf("kind %q: no durable writer, so the kind is inadmissible", k.Name)
	}
	// Replacing a kind must not leave its old format claims pointing at it.
	if prev, ok := r.kinds[k.Name]; ok {
		for _, f := range prev.Formats {
			if r.byFormat[f] == k.Name {
				delete(r.byFormat, f)
			}
		}
	}
	r.kinds[k.Name] = k
	for _, f := range k.Formats {
		r.byFormat[f] = k.Name
	}
	return nil
}

// LookupFormat returns the kind that renders a durable `format` value.
//
// This is the reverse of registration and the lookup a server needs: a result read from
// a notebook carries `format=csv`, not `kind=table`.
func (r *Registry) LookupFormat(format string) (Kind, bool) {
	name, ok := r.byFormat[format]
	if !ok {
		return Kind{}, false
	}
	k, ok := r.kinds[name]
	return k, ok
}

// Formats returns every durable `format` value the registry can render, sorted.
//
// The empty value is omitted: it and "text" name the same kind (§6), and a list
// offered to a user wants the one that can be written down. A tool registering its
// own kind appears here without telling anyone, which is the point — the
// alternative is a UI with a hard-coded list that drifts from what actually
// renders.
func (r *Registry) Formats() []string {
	out := make([]string, 0, len(r.byFormat))
	for f := range r.byFormat {
		if f == "" {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the named kind.
func (r *Registry) Lookup(name string) (Kind, bool) {
	k, ok := r.kinds[name]
	return k, ok
}

// Names returns every registered kind name, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.kinds))
	for name := range r.kinds {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// durableText accepts a string or []byte payload and persists it as a plain `output`
// fence with no `format` key — absent and "text" mean the same thing (§6).
func durableText(payload any) (Durable, error) {
	switch v := payload.(type) {
	case string:
		return Durable{Inline: &Inline{Body: v}}, nil
	case []byte:
		return Durable{Inline: &Inline{Body: string(v)}}, nil
	default:
		return Durable{}, fmt.Errorf("kind %q: payload is %T, want string or []byte", Text, payload)
	}
}

// TablePayload is the payload for the `table` kind.
//
// The executor declares which serialisation it emits; the kit does not transcode
// between csv and jsonl (rendering contract §2.2).
type TablePayload struct {
	// Format is CSV or JSONL.
	Format string
	// Body is the serialised table: RFC 4180 with a header row for csv, one JSON
	// object per line for jsonl.
	Body string
}

func durableTable(payload any) (Durable, error) {
	v, ok := payload.(TablePayload)
	if !ok {
		if p, isPtr := payload.(*TablePayload); isPtr && p != nil {
			v = *p
		} else {
			return Durable{}, fmt.Errorf("kind %q: payload is %T, want kind.TablePayload", Table, payload)
		}
	}
	switch v.Format {
	case CSV, JSONL:
	default:
		return Durable{}, fmt.Errorf("kind %q: format %q is not %q or %q", Table, v.Format, CSV, JSONL)
	}
	return Durable{Inline: &Inline{Format: v.Format, Body: v.Body}}, nil
}
