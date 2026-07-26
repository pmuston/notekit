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
	"strings"
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
func (e *ShellExecutor) Open(ctx context.Context, nb exec.Notebook) (exec.Session, error) {
	path, err := osexec.LookPath(e.shell)
	if err != nil {
		return nil, fmt.Errorf("clinote: locating %s: %w", e.shell, err)
	}

	// A pipe on stdin, the pty on stdout and stderr. This asymmetry is the whole
	// trick, and omitting -i is not enough on its own:
	//
	// A shell decides it is interactive when **stdin is a terminal**, whatever flags
	// it was given. An interactive zsh then sources .zshrc, sets a prompt, and runs
	// its line editor — which re-enables echo after `stty -echo`, redraws the prompt
	// before every command, and emits bracketed-paste toggles. All of it lands in the
	// cell's captured output. bash under the same setup happens to stay quiet, which
	// is exactly why this went unnoticed until someone ran it with zsh.
	//
	// Feeding stdin from a pipe makes the shell non-interactive for real: no rc file,
	// no prompt, no line editor, and nothing to echo. Keeping the pty on stdout means
	// programs still see a terminal and emit colour, which is the reason for a pty at
	// all (harvest F12). State still carries between cells, because it is still one
	// long-lived shell reading a stream (harvest R1).
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("clinote: allocating a pty: %w", err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		ptmx.Close()
		tty.Close()
		return nil, fmt.Errorf("clinote: creating the command pipe: %w", err)
	}

	cmd := osexec.Command(path)
	if dir := dirOf(nb.Path); dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = stdinR
	cmd.Stdout = tty
	cmd.Stderr = tty
	// A new session with the pty as controlling terminal, so TIOCGPGRP on the master
	// reports the foreground process group and an interrupt reaches the right one.
	// Ctty indexes the child's descriptors, where 1 is stdout — the pty.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	// Belt and braces: a non-interactive shell should not read these anyway.
	cmd.Env = append(os.Environ(),
		"PS1=", "PS2=", "PS3=", "PS4=",
		"PROMPT=", "RPROMPT=", "PROMPT_COMMAND=",
		"HISTFILE=/dev/null",
		"TERM="+e.term,
	)

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		stdinR.Close()
		stdinW.Close()
		return nil, fmt.Errorf("clinote: starting %s under a pty: %w", e.shell, err)
	}
	// The child holds its own copies; the parent keeps only the master and the write
	// end. Leaving the slave open here would stop the master ever seeing EOF.
	_ = tty.Close()
	_ = stdinR.Close()

	s := &shellSession{
		cmd:      cmd,
		pty:      ptmx,
		in:       stdinW,
		sentinel: newSentinel(),
		cap:      e.cap,
	}
	if err := s.init(); err != nil {
		// Close is the one path that tears everything down correctly, including the
		// stdin pipe and the child.
		_ = s.Close(ctx)
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
	mu  sync.Mutex
	cmd *osexec.Cmd
	// pty is the master side: the session reads output from it. It is never written
	// to — commands go down `in`.
	pty *os.File
	// in is the write end of the shell's stdin pipe. Closing it gives the shell EOF,
	// which is how it exits cleanly.
	in       *os.File
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
	// Two settings, each for a specific failure:
	//
	// `trap ':' INT` keeps the session alive through a cancellation. A non-interactive
	// shell has no job control, so a command runs in the shell's own process group and
	// an interrupt aimed at that group hits the shell too — which would kill it. A
	// *handler* rather than an ignore is essential: POSIX resets handled traps to the
	// default in a child, so the command still dies, while `trap '' INT` would be
	// inherited as ignore and make the command unkillable.
	//
	// `set -m` would be the textbook answer here and is deliberately not used: it makes
	// zsh stall on startup under this arrangement, presumably taking terminal control it
	// cannot have. Found by trying it.
	//
	// `stty` reads its settings from stdin, which is now a pipe, so it is pointed at the
	// controlling terminal instead. -onlcr stops the tty translating newlines to CRLF,
	// which would otherwise put a stray carriage return on every captured line.
	setup := "trap ':' INT; stty -onlcr < /dev/tty 2>/dev/null; " +
		"unset PROMPT_COMMAND HISTFILE\n"
	if _, err := s.in.Write([]byte(setup)); err != nil {
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
	if _, err := s.in.Write([]byte(full)); err != nil {
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

// drain consumes any bytes left over between commands, so a stray write — a process
// backgrounded by the previous cell, say — cannot be attributed to the next one.
//
// It must read what is *already* there and never wait for more, and doing that portably
// is the whole difficulty. Read deadlines are not the answer: a pty master does not
// support them on darwin (SetReadDeadline fails outright, so this returned having drained
// nothing), and on linux the deadline is accepted and then ignored, because creack/pty
// performs its ioctls through File.Fd() — which puts the descriptor back into blocking
// mode while leaving the poller state intact. A blocking read then never consults the
// deadline and waits forever. That hung session init for the full test timeout in CI.
//
// So the bound comes from the descriptor instead of from a timer: O_NONBLOCK guarantees
// EAGAIN rather than a wait, which is a property of the syscall rather than of any
// platform's poller. SyscallConn().Control hands over the descriptor without disturbing
// its blocking mode, unlike Fd(). The original flags go back on afterwards because
// readUntilSentinel wants exactly the opposite — it blocks on purpose, waiting for a
// sentinel that is definitely coming.
func (s *shellSession) drain() {
	rc, err := s.pty.SyscallConn()
	if err != nil {
		return // a closed session has nothing to drain
	}
	_ = rc.Control(func(fd uintptr) {
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil {
			return
		}
		if flags&unix.O_NONBLOCK == 0 {
			if _, err := unix.FcntlInt(fd, unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
				return
			}
			defer func() { _, _ = unix.FcntlInt(fd, unix.F_SETFL, flags) }()
		}
		buf := make([]byte, 4096)
		for {
			n, err := unix.Read(int(fd), buf)
			if n <= 0 || err != nil {
				return // EAGAIN once the pending bytes are gone
			}
		}
	})
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
				// Trim a trailing CR: the tty translates LF to CRLF unless
				// -onlcr took effect, and a status the parser refuses to read
				// would look like a broken session rather than a stray byte.
				digits := strings.TrimRight(string(line[colon+1:]), "\r \t")
				status, perr := strconv.Atoi(digits)
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
	// Control rather than Fd(): Fd() has the side effect of putting the descriptor into
	// blocking mode, which is what made drain hang for the length of the test timeout.
	// Control also fails cleanly on an already-closed file instead of handing back a
	// descriptor number that may since have been reused.
	rc, err := s.pty.SyscallConn()
	if err != nil {
		return fmt.Errorf("clinote: pty descriptor: %w", err)
	}
	var pgrp int
	var ioctlErr error
	if err := rc.Control(func(fd uintptr) {
		pgrp, ioctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	}); err != nil {
		return fmt.Errorf("clinote: pty descriptor: %w", err)
	}
	if ioctlErr != nil {
		return fmt.Errorf("clinote: TIOCGPGRP: %w", ioctlErr)
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
	// EOF on stdin asks the shell to exit; closing the pty is what unblocks a read
	// stuck on a hung command.
	_ = s.in.Close()
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
