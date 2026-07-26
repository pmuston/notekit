//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	// A pty is what makes an interactive shell behave like one: state persists
	// between cells (harvest R1) and programs that check isatty emit colour, which
	// is the whole point of the kit's live-ANSI path (harvest F12). The alternative,
	// pipes, loses both. creack/pty is the de facto standard wrapper over
	// posix_openpt and friends; the only other option is hand-rolling those ioctls,
	// which is more code for the same result. Justified per the dependency policy.
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
)

// Lang is the info-string tag clinote claims.
const Lang = "sh"

// tailWindow is the rolling buffer scanned for the sentinel.
//
// It is kept separate from the captured body so that truncation can never hide the
// sentinel: output past the cap is dropped from the body but reading continues, and the
// window always holds enough of the tail to spot the marker when it arrives. Getting
// this wrong desynchronises the shell — the next cell would read the previous cell's
// sentinel — which is why the two buffers exist rather than one.
const tailWindow = 8192

// ShellExecutor runs `sh` cells in a persistent shell under a pty.
//
// All pty knowledge lives in this file and in no kit package (kit spec §4). The kit sees
// only an [exec.Executor]: whether a session is a pty child or a network database never
// surfaces above that boundary (harvest D5).
type ShellExecutor struct {
	shell string
	term  string
	cap   int
}

// NewShellExecutor returns an executor for the named shell, which must be bash or zsh.
//
// Restricting the shell is deliberate rather than lazy: the sentinel protocol depends on
// `stty`, `printf` and `$?` behaving as those two do, and silently accepting a shell that
// prompts differently would desynchronise the session in ways that look like corrupt
// output rather than a configuration error.
func NewShellExecutor(shell, term string, outputCap int) (*ShellExecutor, error) {
	switch shell {
	case "bash", "zsh":
	default:
		return nil, fmt.Errorf("clinote: unsupported shell %q (want bash or zsh)", shell)
	}
	if _, err := osexec.LookPath(shell); err != nil {
		return nil, fmt.Errorf("clinote: locating %s: %w", shell, err)
	}
	if outputCap <= 0 {
		return nil, fmt.Errorf("clinote: output cap must be positive")
	}
	return &ShellExecutor{shell: shell, term: term, cap: outputCap}, nil
}

func (e *ShellExecutor) Lang() string { return Lang }

// Open starts a shell under a pty, one per notebook (harvest R1).
//
// The notebook's directory becomes the shell's working directory, so relative paths in a
// cell mean what a reader expects. The file itself is never written to — persistence is
// package run's job.
func (e *ShellExecutor) Open(ctx context.Context, notebookPath string) (exec.Session, error) {
	path, err := osexec.LookPath(e.shell)
	if err != nil {
		return nil, fmt.Errorf("clinote: locating %s: %w", e.shell, err)
	}

	// Non-interactive on purpose. An interactive shell runs a line editor that owns
	// the terminal: zsh's ZLE re-enables echo after `stty -echo` and redraws a prompt
	// before every command, and both land in the cell's captured output. Dropping -i
	// removes the line editor, the prompt, and the echo at their source rather than
	// asking the shell to suppress what it is designed to do. Nothing is lost that a
	// notebook wants — state still carries between cells, because it is still one
	// long-lived shell reading a stream (harvest R1).
	cmd := osexec.Command(path)
	if dir := dirOf(notebookPath); dir != "" {
		cmd.Dir = dir
	}
	// Quiet every prompt the shell might emit. A prompt written between commands
	// would land in a cell's captured output, and the sentinel protocol has no way to
	// tell it apart from the command's own writing.
	cmd.Env = append(os.Environ(),
		"PS1=", "PS2=", "PS3=", "PS4=",
		"PROMPT=", "RPROMPT=", "PROMPT_COMMAND=",
		"HISTFILE=/dev/null",
		"TERM="+e.term,
	)

	p, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("clinote: starting %s under a pty: %w", e.shell, err)
	}

	s := &shellSession{
		cmd:      cmd,
		pty:      p,
		sentinel: newSentinel(),
		cap:      e.cap,
	}
	if err := s.init(); err != nil {
		_ = p.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, err
	}
	return s, nil
}

func dirOf(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := os.Getwd(); err == nil && path == abs {
		return ""
	}
	return filepathDir(path)
}

// shellSession is one notebook's shell.
type shellSession struct {
	// mu serialises Execute. Package run never runs two cells of one notebook at
	// once, but a session must not rely on a caller's promise for its own safety.
	mu       sync.Mutex
	cmd      *osexec.Cmd
	pty      *os.File
	sentinel string
	cap      int

	// closed is atomic and deliberately not guarded by mu. Close must be able to
	// tear the session down while an Execute holds mu on a hung command — a Close
	// that waited for the mutex would deadlock, and since Close runs on the Ctrl-C
	// path, that would wedge the whole process.
	closed atomic.Bool

	// ptyMu guards the pty *handle* against use-after-close, and is held only for
	// the instant it takes to close the pty or read its descriptor — never across a
	// blocking read, because Close must be able to close the pty while a read is
	// blocked on it.
	//
	// Without this, interrupt could read a descriptor Close had already released.
	// That is worse than it sounds: a reused descriptor would make TIOCGPGRP report
	// some other process group, and the SIGINT would go to it.
	ptyMu sync.Mutex
}

// init quiets the shell and then runs a no-op through the sentinel protocol, which
// swallows the shell's banner and the echo of the setup line itself.
func (s *shellSession) init() error {
	// With no line editor there is nothing to fight over the terminal, so this
	// sticks. -onlcr stops the tty translating newlines to CRLF, which would
	// otherwise put a stray carriage return at the end of every captured line.
	setup := "stty -echo -onlcr 2>/dev/null; unset PROMPT_COMMAND HISTFILE\n"
	if _, err := s.pty.Write([]byte(setup)); err != nil {
		return fmt.Errorf("clinote: quieting the shell: %w", err)
	}
	if _, err := s.Execute(context.Background(), exec.Request{Source: "true\n"}); err != nil {
		return fmt.Errorf("clinote: shell init: %w", err)
	}
	return nil
}

func newSentinel() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a predictable sentinel would
		// let cell output forge an exit status.
		panic("clinote: crypto/rand: " + err.Error())
	}
	return "__NOTEKIT_END_" + hex.EncodeToString(b[:]) + "__"
}

// Execute runs one cell in the persistent shell.
//
// A non-zero exit status is a *domain* failure, returned as an [*exec.Error] so the
// runtime persists a first-class `error` block (§7) rather than folding it into output.
func (s *shellSession) Execute(ctx context.Context, req exec.Request) (exec.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return exec.Result{}, errors.New("clinote: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return exec.Result{}, err
	}

	s.drain()

	source := req.Source
	if source != "" && !bytes.HasSuffix([]byte(source), []byte("\n")) {
		source += "\n"
	}
	// stdout and stderr both flow through the pty and interleave as produced, which
	// is exactly what §7 requires of a shell error body. clinote v1 split stderr to a
	// temp file via a `2>` redirect; the format has no place for two streams, so the
	// redirect and the temp file both go away.
	full := source + "printf '\\n" + s.sentinel + ":%d\\n' \"$?\"\n"
	if _, err := s.pty.Write([]byte(full)); err != nil {
		return exec.Result{}, fmt.Errorf("clinote: writing to the shell: %w", err)
	}

	// Cancellation interrupts the running command rather than abandoning the read:
	// the sentinel still has to be consumed, or the next cell would read this cell's
	// marker and the session would be permanently out of step.
	//
	// Execute waits for the watcher before returning. A watcher left running could
	// call interrupt after the session was closed, and signal whatever process group
	// had inherited the descriptor.
	var watcher sync.WaitGroup
	stop := make(chan struct{})
	if ctx.Done() != nil {
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			select {
			case <-ctx.Done():
				_ = s.interrupt()
			case <-stop:
			}
		}()
	}

	body, status, truncated, err := s.readUntilSentinel()
	close(stop)
	watcher.Wait()
	if err != nil {
		return exec.Result{}, err
	}
	// Report cancellation only after the session is back in sync.
	if err := ctx.Err(); err != nil {
		return exec.Result{}, err
	}

	// Raw, not stripped: ANSI stripping is the format layer's job (doc.StripANSI, via
	// package run), and stripping here would destroy the colour the browser renders.
	// clinote v1 stripped in the runner, which is the responsibility the kit inverts.
	out := string(body)

	if status != 0 {
		return exec.Result{Truncated: truncated},
			exec.NewError(status, "%s", out)
	}

	format := ""
	if req.Meta != nil {
		if e, ok := req.Meta.Get("format"); ok {
			format = e.Value
		}
	}
	switch format {
	case kind.CSV, kind.JSONL:
		// The proven two-axis usage: the info string says what kind of block this is
		// and how to interpret its body (harvest open question 1).
		return exec.Result{
			Kind:      kind.Table,
			Payload:   kind.TablePayload{Format: format, Body: out},
			Truncated: truncated,
		}, nil
	default:
		return exec.Result{Kind: kind.Text, Payload: out, Truncated: truncated}, nil
	}
}

// drain consumes any bytes left over between commands, so a stray write cannot be
// attributed to the next cell.
func (s *shellSession) drain() {
	if err := s.pty.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		return
	}
	defer func() { _ = s.pty.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n == 0 || err != nil {
			return
		}
	}
}

// readUntilSentinel reads until the sentinel line arrives, returning the body before it,
// the exit status, and whether output was dropped.
//
// Reading continues past the cap so the shell stays in sync; only the body is bounded.
func (s *shellSession) readUntilSentinel() ([]byte, int, bool, error) {
	marker := []byte("\n" + s.sentinel + ":")

	var body bytes.Buffer
	tail := make([]byte, 0, tailWindow)
	truncated := false
	chunk := make([]byte, 4096)

	for {
		n, err := s.pty.Read(chunk)
		if n > 0 {
			data := chunk[:n]

			if room := s.cap - body.Len(); room > 0 {
				if len(data) <= room {
					body.Write(data)
				} else {
					body.Write(data[:room])
					truncated = true
				}
			} else {
				truncated = true
			}

			if len(tail)+len(data) <= tailWindow {
				tail = append(tail, data...)
			} else {
				combined := append(tail, data...)
				tail = append(tail[:0], combined[len(combined)-tailWindow:]...)
			}

			if idx := bytes.Index(tail, marker); idx >= 0 {
				after := idx + 1 // past the newline printf injects
				nl := bytes.IndexByte(tail[after:], '\n')
				if nl < 0 {
					continue // the status digits have not all arrived
				}
				line := tail[after : after+nl]
				colon := bytes.IndexByte(line, ':')
				if colon < 0 {
					return nil, 0, truncated,
						fmt.Errorf("clinote: malformed sentinel line %q", line)
				}
				status, perr := strconv.Atoi(string(line[colon+1:]))
				if perr != nil {
					return nil, 0, truncated,
						fmt.Errorf("clinote: parsing exit status from %q: %w", line, perr)
				}

				// The marker may also sit in the body when output was short.
				out := body.Bytes()
				if i := bytes.Index(out, marker); i >= 0 {
					out = out[:i]
				}
				return out, status, truncated, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, 0, truncated,
					fmt.Errorf("clinote: the shell exited: %w", io.ErrUnexpectedEOF)
			}
			return nil, 0, truncated, fmt.Errorf("clinote: reading from the shell: %w", err)
		}
	}
}

// interrupt sends SIGINT to the pty's foreground process group, so the running command
// dies and the shell prints the sentinel with a non-zero status.
func (s *shellSession) interrupt() error {
	if s.pty == nil {
		return errors.New("clinote: session not started")
	}
	s.ptyMu.Lock()
	defer s.ptyMu.Unlock()
	// Close sets closed before taking ptyMu, so reaching here with it set means the
	// descriptor is gone and there is nothing left to interrupt.
	if s.closed.Load() {
		return nil
	}
	pgrp, err := unix.IoctlGetInt(int(s.pty.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return fmt.Errorf("clinote: TIOCGPGRP: %w", err)
	}
	return syscall.Kill(-pgrp, syscall.SIGINT)
}

// Close terminates the shell and releases the pty.
//
// It does not take the mutex, on purpose. A command blocked reading input clinote never
// sends would leave Execute holding mu indefinitely, so a Close that waited would
// deadlock — and Close runs on the Ctrl-C path, which would wedge the process. Closing
// the pty is what unblocks the stuck read, letting Execute unwind on its own.
func (s *shellSession) Close(context.Context) error {
	if s.closed.Swap(true) {
		return nil
	}
	s.ptyMu.Lock()
	_ = s.pty.Close()
	s.ptyMu.Unlock()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	done := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// The shell is gone or detached; do not hold up shutdown for it.
	}
	return nil
}
