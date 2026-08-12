package kind

import (
	"strings"
	"testing"
)

func TestLiveText(t *testing.T) {
	got, err := liveText(LiveInput{Body: "hello\nworld\n"})
	if err != nil {
		t.Fatalf("liveText: %v", err)
	}
	want := "<pre class=\"nk-output\">hello\nworld\n</pre>"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLiveTextColoursAndEscapes(t *testing.T) {
	got, err := liveText(LiveInput{Body: "\x1b[31m<b>\x1b[0m"})
	if err != nil {
		t.Fatal(err)
	}
	// Colour becomes a span; the body's own markup is escaped.
	if !strings.Contains(string(got), `<span class="ansi-fg-1">&lt;b&gt;</span>`) {
		t.Errorf("got %q", got)
	}
}

func TestLiveError(t *testing.T) {
	got := LiveError("\x1b[31mboom\x1b[0m <x>")
	s := string(got)
	if !strings.Contains(s, `class="nk-error"`) {
		t.Errorf("error class missing: %q", s)
	}
	if !strings.Contains(s, "&lt;x&gt;") {
		t.Errorf("body not escaped: %q", s)
	}
}

func TestLiveTableCSV(t *testing.T) {
	got, err := liveTable(LiveInput{Format: CSV, Body: "name,size\nalpha,10\nbeta,2\n"})
	if err != nil {
		t.Fatalf("liveTable: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`class="nk-table"`, `data-sortable="true"`,
		`<th data-col="0" tabindex="0">name</th>`,
		`<th data-col="1" tabindex="0">size</th>`,
		"<td>alpha</td><td>10</td>",
		"<td>beta</td><td>2</td>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("table missing %q:\n%s", want, s)
		}
	}
}

func TestLiveTableEscapesCells(t *testing.T) {
	got, err := liveTable(LiveInput{Format: CSV, Body: "col\n\"<script>\"\n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "<script>") {
		t.Errorf("cell content not escaped: %s", got)
	}
	if !strings.Contains(string(got), "&lt;script&gt;") {
		t.Errorf("expected escaped content: %s", got)
	}
}

// TestLiveTableShortRowsPadded: a result is data, not a schema, so a ragged row is
// displayed rather than rejected.
func TestLiveTableShortRowsPadded(t *testing.T) {
	got, err := liveTable(LiveInput{Format: CSV, Body: "a,b,c\n1\n2,3\n"})
	if err != nil {
		t.Fatalf("liveTable: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "<td>1</td><td></td><td></td>") {
		t.Errorf("short row not padded:\n%s", s)
	}
	if !strings.Contains(s, "<td>2</td><td>3</td><td></td>") {
		t.Errorf("short row not padded:\n%s", s)
	}
}

func TestLiveTableJSONL(t *testing.T) {
	body := `{"name":"alpha","size":10}` + "\n" +
		`{"size":2,"name":"beta","extra":true}` + "\n"
	got, err := liveTable(LiveInput{Format: JSONL, Body: body})
	if err != nil {
		t.Fatalf("liveTable: %v", err)
	}
	s := string(got)
	// Columns are first-seen order, so a key a later row introduces is appended
	// rather than reordering the table.
	nameAt := strings.Index(s, ">name<")
	sizeAt := strings.Index(s, ">size<")
	extraAt := strings.Index(s, ">extra<")
	if nameAt < 0 || sizeAt < 0 || extraAt < 0 {
		t.Fatalf("columns missing:\n%s", s)
	}
	if !(nameAt < sizeAt && sizeAt < extraAt) {
		t.Errorf("columns not in first-seen order:\n%s", s)
	}
	if !strings.Contains(s, "<td>alpha</td><td>10</td><td></td>") {
		t.Errorf("first row wrong:\n%s", s)
	}
	if !strings.Contains(s, "<td>beta</td><td>2</td><td>true</td>") {
		t.Errorf("second row wrong:\n%s", s)
	}
}

func TestLiveTableJSONLValueTypes(t *testing.T) {
	body := `{"s":"x","n":1.5,"i":42,"b":false,"z":null,"arr":[1,2],"obj":{"k":"v"}}` + "\n"
	got, err := liveTable(LiveInput{Format: JSONL, Body: body})
	if err != nil {
		t.Fatalf("liveTable: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"<td>x</td>", "<td>1.5</td>", "<td>42</td>", "<td>false</td>",
		// A nested value keeps its JSON form: honest about what the data is.
		"[1,2]", "{&#34;k&#34;:&#34;v&#34;}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

// TestLiveTableMalformedFallsBackToText: the body is what the run produced, and hiding
// it would help nobody.
func TestLiveTableMalformedFallsBackToText(t *testing.T) {
	tests := []struct{ name, format, body string }{
		{"unclosed csv quote", CSV, "col\n\"unclosed\nmore\n"},
		{"empty csv", CSV, ""},
		{"bad json", JSONL, "{not json}\n"},
		{"no json objects", JSONL, "\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := liveTable(LiveInput{Format: tt.format, Body: tt.body})
			if err != nil {
				t.Fatalf("liveTable returned an error rather than falling back: %v", err)
			}
			if !strings.Contains(string(got), "nk-malformed") {
				t.Errorf("not flagged as malformed:\n%s", got)
			}
		})
	}
}

func TestLiveTableUnknownFormat(t *testing.T) {
	// An unknown serialisation is an error: the kit does not transcode or guess.
	if _, err := liveTable(LiveInput{Format: "parquet", Body: "a,b\n"}); err == nil {
		t.Error("want an error for an unrenderable format")
	}
}

func TestScalarString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"text", "text"},
		{true, "true"},
		{false, "false"},
		{float64(42), "42"},
		{float64(1.5), "1.5"},
		{float64(1e21), "1e+21"},
	}
	for _, tt := range tests {
		if got := scalarString(tt.in); got != tt.want {
			t.Errorf("scalarString(%#v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
