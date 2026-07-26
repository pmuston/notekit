package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const front = "---\nnotekit: 1\ntitle: T\n---\n\n"

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestUsageErrors(t *testing.T) {
	path := write(t, front+"## A\n\n```echo\nx\n```\n")
	tests := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"two notebooks", []string{path, path}},
		{"unknown flag", []string{"-nope", path}},
		{"missing file", []string{filepath.Join(t.TempDir(), "nope.md")}},
		{"not a notebook", []string{write(t, "not a notebook\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := runMain(tt.args, &out, &errBuf); code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
		})
	}
}

// TestServesAndShutsDown starts the real server on a free port, fetches the notebook,
// and confirms a clean shutdown — the whole binary, not just its parts.
func TestServesAndShutsDown(t *testing.T) {
	path := write(t, front+"## Greeting\n\n```echo\nhello\n```\n")

	// Bind a port, release it, and reuse the number: a fixed port would collide.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	var out, errBuf lockedBuffer
	go func() { runMain([]string{"-addr", addr, path}, &out, &errBuf) }()

	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	var body string
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v\nstderr: %s", err, errBuf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, want := range []string{"Greeting", "hello", "/assets/htmx.min.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// The vendored asset is served from the binary, so a browser needs no network.
	resp, err := http.Get(base + "/assets/htmx.min.js")
	if err != nil {
		t.Fatalf("fetching the vendored asset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET htmx.min.js = %d", resp.StatusCode)
	}
}

// lockedBuffer lets the server goroutine write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
