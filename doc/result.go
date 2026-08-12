package doc

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pmuston/notekit/meta"
)

// Reserved key orders for canonical result metadata (§6, §7).
//
// These live here rather than in meta because meta holds no key semantics: the
// orders belong to whichever package *writes* result blocks, which is this one.
// The `error` order puts status first, matching §7's example.
var (
	OutputKeyOrder = []string{"format", "run", "tool", "truncated"}
	ErrorKeyOrder  = []string{"status", "run", "tool", "truncated"}
)

// OutputCap is the durable capture limit (harvest R4, §6).
const OutputCap = 1 << 20 // 1 MiB

// TruncationLine returns the marker a tool appends when it capped output (§6).
func TruncationLine(n int) string {
	return "[notekit: output truncated at " + strconv.Itoa(n) + " bytes]"
}

// Truncate caps body at n bytes and appends the truncation marker, reporting
// whether it did so.
//
// Truncation happens *before* fence length is computed — see [FenceLen] — because
// cutting the body can leave a partial backtick run that the fence must still clear.
// Composing Truncate then a block writer enforces that order by construction.
func Truncate(body string, n int) (string, bool) {
	if len(body) <= n {
		return body, false
	}
	cut := body[:n]
	// Do not split a multi-byte rune: back off to a rune boundary.
	for len(cut) > 0 && !utf8Start(cut[len(cut)-1]) && !isASCII(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	if !strings.HasSuffix(cut, "\n") {
		cut += "\n"
	}
	return cut + TruncationLine(n) + "\n", true
}

func isASCII(c byte) bool   { return c < 0x80 }
func utf8Start(c byte) bool { return c&0xC0 == 0xC0 }

// FenceLen returns the number of backticks a fence needs to enclose body safely:
// max(3, longest backtick run in body + 1), per §6. Applies to `error` equally.
func FenceLen(body string) int {
	longest, run := 0, 0
	for i := 0; i < len(body); i++ {
		if body[i] == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	if longest+1 > 3 {
		return longest + 1
	}
	return 3
}

// StripANSI removes terminal escape sequences and stray control characters, leaving
// the plain text that §6 requires of a durable body.
//
// Scope, resolving the question the implementation plan raised as §8.5: CSI
// sequences (ESC [ … final byte), OSC sequences (ESC ] … BEL or ST), and any other
// two-byte ESC sequence are removed; remaining control characters are dropped except
// tab and newline — a shell executor emits far more than colour, and the durable form
// is the plain form (harvest F12). CRLF therefore becomes LF.
//
// Cursor movement within a line is replayed first ([ReplayRedraws]), so a line that
// rewrote itself persists as the text it finally showed. This function used to delete
// the carriage return and keep every frame, which is how a few seconds of spinner
// reached the file as a paragraph of itself with the real output buried at the end.
func StripANSI(s string) string {
	s = ReplayRedraws(s)

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			if c == '\t' || c == '\n' || c >= 0x20 {
				b.WriteByte(c)
			}
			i++
			continue
		}
		// ESC: consume the sequence.
		if i+1 >= len(s) {
			i++ // lone ESC at end of input
			continue
		}
		switch s[i+1] {
		case '[': // CSI: parameters then a final byte in @–~
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
		case ']': // OSC: terminated by BEL or ESC \
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			// ESC I… F: zero or more intermediate bytes (0x20–0x2F) then one
			// final byte (0x30–0x7E). `ESC ( B` — select ASCII charset — is
			// three bytes, so consuming a fixed two would leak the final byte
			// into the output.
			j := i + 1
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
		}
	}
	return b.String()
}

// normaliseBody applies §6's body rules: ANSI stripped, trailing newline normalised
// to exactly one. An empty body stays empty so the fence has no content lines.
func normaliseBody(body string) string {
	s := StripANSI(body)
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// ResultBlock is a result to be written as an `output` or `error` fence (§6, §7).
//
// Body is the captured text; it is ANSI-stripped and newline-normalised on write.
// Truncate it first if the runtime capped it, so the marker is inside the body when
// the fence length is computed.
type ResultBlock struct {
	Form ResultForm // ResultOutput or ResultError

	Body string

	// Format is the result kind for `output` (§6): text, csv, jsonl. Empty means
	// text, and the key is omitted — absent and `text` mean the same thing.
	Format string

	// Status is the domain's numeric error code for `error` (§7), when it has one.
	Status *int

	Run       time.Time // RFC 3339 UTC when non-zero
	Tool      string    // name/version
	Truncated bool      // adds the `truncated` flag key

	// Extra carries passthrough keys, emitted after the reserved ones.
	Extra []meta.Entry
}

// String renders the block, applying fence-length safety.
func (r ResultBlock) String() (string, error) {
	var tag string
	var order []string
	switch r.Form {
	case ResultOutput:
		tag, order = "output", OutputKeyOrder
	case ResultError:
		tag, order = "error", ErrorKeyOrder
	default:
		return "", fmt.Errorf("notekit: %v is not an inline result form", r.Form)
	}

	var entries []meta.Entry
	if r.Form == ResultOutput && r.Format != "" && r.Format != "text" {
		entries = append(entries, meta.Entry{Key: "format", Value: r.Format})
	}
	if r.Form == ResultError && r.Status != nil {
		entries = append(entries, meta.Entry{Key: "status", Value: strconv.Itoa(*r.Status)})
	}
	if !r.Run.IsZero() {
		// Quoted to match the spec's examples, though the value needs no quoting.
		entries = append(entries, meta.Entry{
			Key: "run", Value: r.Run.UTC().Format(time.RFC3339), Quoted: true,
		})
	}
	if r.Tool != "" {
		entries = append(entries, meta.Entry{Key: "tool", Value: r.Tool, Quoted: true})
	}
	if r.Truncated {
		entries = append(entries, meta.Entry{Key: "truncated", Flag: true})
	}
	entries = append(entries, r.Extra...)

	info, err := meta.Format(tag, entries, order)
	if err != nil {
		return "", fmt.Errorf("notekit: building %s block: %w", tag, err)
	}

	body := normaliseBody(r.Body)
	fence := strings.Repeat("`", FenceLen(body))
	return fence + info + "\n" + body + fence + "\n", nil
}

// SidecarRef is a sidecar result reference: provenance comment plus image link (§8).
type SidecarRef struct {
	Kind string // the result kind, e.g. graph
	Dest string // path to the artifact, relative to the notebook
	Alt  string // image alt text; the cell's heading reads well here

	Run   time.Time
	Tool  string
	Extra []meta.Entry
}

// String renders the two-line construct.
//
// The attributes use the §9 grammar with the braces removed and nothing else
// changed, so they stay comma-separated.
func (r SidecarRef) String() (string, error) {
	if r.Dest == "" {
		return "", fmt.Errorf("notekit: sidecar reference needs a destination")
	}
	entries := []meta.Entry{}
	if r.Kind != "" {
		entries = append(entries, meta.Entry{Key: "kind", Value: r.Kind})
	}
	if !r.Run.IsZero() {
		entries = append(entries, meta.Entry{
			Key: "run", Value: r.Run.UTC().Format(time.RFC3339), Quoted: true,
		})
	}
	if r.Tool != "" {
		entries = append(entries, meta.Entry{Key: "tool", Value: r.Tool, Quoted: true})
	}
	entries = append(entries, r.Extra...)

	// meta.Format needs a tag; the synthetic one is stripped back off, leaving
	// exactly the attribute list §8 describes.
	info, err := meta.Format(provenanceTag, entries, []string{"kind", "run", "tool"})
	if err != nil {
		return "", fmt.Errorf("notekit: building sidecar reference: %w", err)
	}
	attrs := strings.TrimPrefix(info, provenanceTag)
	attrs = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(attrs), "{"), "}"))

	comment := "<!-- " + provenancePrefix
	if attrs != "" {
		comment += " " + attrs
	}
	comment += " -->"

	return comment + "\n![" + r.Alt + "](" + r.Dest + ")\n", nil
}

// SidecarName returns the sidecar filename for a cell: `<slug>--<id>.<ext>`, or
// `<id>.<ext>` when the slug is empty (§8).
func SidecarName(slug, id, ext string) string {
	ext = strings.TrimPrefix(ext, ".")
	base := id
	if slug != "" {
		base = slug + "--" + id
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// SplitSidecarName recovers the slug and id from a sidecar filename by splitting on
// the **last** `--` (§8). ok is false when the name carries no id.
func SplitSidecarName(name string) (slug, id string, ok bool) {
	base := name
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	if i := strings.LastIndex(base, "--"); i >= 0 {
		slug, id = base[:i], base[i+2:]
	} else {
		slug, id = "", base
	}
	if !ValidID(id) {
		return "", "", false
	}
	return slug, id, true
}
