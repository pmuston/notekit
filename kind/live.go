package kind

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"sort"
	"strconv"
	"strings"
)

// LiveInput is what a live renderer renders from.
//
// Body is a *durable* body, not an executor payload. That is deliberate: a server
// renders a notebook read from disk far more often than it renders a fresh run, so the
// durable form has to be the renderer's input or every kind would need two code paths.
// Freshness is expressed by *which* body the caller passes — [run.Scheduler.LiveBody]
// for a result this process produced, which still carries ANSI, or the persisted body,
// which does not.
type LiveInput struct {
	// Format is the durable `format` value, so a kind serving several
	// serialisations knows which one it has.
	Format string

	// Body is the result body as text.
	Body string

	// Truncated reports that the output hit the cap, so a renderer can say so
	// without the caller parsing the marker back out.
	Truncated bool
}

// LiveFunc renders a result for the browser.
//
// The returned HTML is inserted unescaped, so a renderer is responsible for escaping
// everything that came from a result body. Returning [template.HTML] rather than string
// makes that responsibility explicit at the type level.
type LiveFunc func(in LiveInput) (template.HTML, error)

// liveText renders plain text, converting ANSI colour to spans (harvest F12).
func liveText(in LiveInput) (template.HTML, error) {
	return template.HTML(`<pre class="nk-output">` + ANSIToHTML(in.Body) + `</pre>`), nil
}

// liveTable renders csv or jsonl as a sortable table (harvest V1).
//
// Sortability is client-side and therefore live-only: it degrades to an ordinary table
// with JavaScript off, and to a fenced block of csv on GitHub. The table renders what
// was persisted and never a fuller version.
func liveTable(in LiveInput) (template.HTML, error) {
	var header []string
	var rows [][]string
	var err error

	switch in.Format {
	case CSV:
		header, rows, err = parseCSV(in.Body)
	case JSONL:
		header, rows, err = parseJSONL(in.Body)
	default:
		return "", fmt.Errorf("kind %q: cannot render format %q live", Table, in.Format)
	}
	if err != nil {
		// A malformed table is shown as text rather than as an error page: the body
		// is what the run actually produced, and hiding it would be less useful
		// than showing it unformatted.
		return template.HTML(`<pre class="nk-output nk-malformed">` +
			ANSIToHTML(in.Body) + `</pre>`), nil
	}

	var b strings.Builder
	b.WriteString(`<table class="nk-table" data-sortable="true"><thead><tr>`)
	for i, h := range header {
		b.WriteString(`<th data-col="` + strconv.Itoa(i) + `" tabindex="0">`)
		b.WriteString(template.HTMLEscapeString(h))
		b.WriteString(`</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		for i := range header {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(`<td>`)
			b.WriteString(template.HTMLEscapeString(cell))
			b.WriteString(`</td>`)
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table>`)
	return template.HTML(b.String()), nil
}

// parseCSV reads RFC 4180 with a required header row (§2.2).
func parseCSV(body string) ([]string, [][]string, error) {
	r := csv.NewReader(strings.NewReader(body))
	// Rows of differing length are tolerated: a result is data, not a schema, and
	// refusing to display it would be less useful than padding it.
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil, fmt.Errorf("empty csv")
		}
		return nil, nil, err
	}
	var rows [][]string
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// parseJSONL reads one JSON object per line, flattening to columns by first-seen key
// order (harvest V1).
//
// First-seen order rather than sorted: it preserves whatever the executor considered
// natural, and a table whose columns reorder when a later row introduces a key would be
// worse than one that appends.
func parseJSONL(body string) ([]string, [][]string, error) {
	var header []string
	seen := map[string]int{}
	var objects []map[string]any

	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		objects = append(objects, obj)

		// Sort each object's own keys so a single object's columns are stable;
		// across objects, first-seen order wins.
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = len(header)
				header = append(header, k)
			}
		}
	}
	if len(header) == 0 {
		return nil, nil, fmt.Errorf("no json objects")
	}

	rows := make([][]string, 0, len(objects))
	for _, obj := range objects {
		row := make([]string, len(header))
		for k, v := range obj {
			row[seen[k]] = scalarString(v)
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// scalarString renders a JSON value as a table cell. Nested values keep their JSON
// form, which is honest about what the data is rather than flattening it further.
func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// liveError renders an error body. It is not a registered kind — the format fixes the
// error form so no kind may vary it — but a server needs to render one, so it lives
// here beside its siblings.
func LiveError(body string) template.HTML {
	return template.HTML(`<pre class="nk-error">` + ANSIToHTML(body) + `</pre>`)
}
