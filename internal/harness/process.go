package harness

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Shared subprocess plumbing for the real Claude and Codex drivers. It launches
// one native harness process in the workspace, reads its stdout as bounded,
// newline-delimited frames, and drains its stderr into a bounded ring that is
// NEVER surfaced as an event or a reason: harnesses write noisy diagnostic logs
// to stderr (Codex emits skill-load ERROR lines there), and forwarding any of
// it risks leaking model or credential text past the seam.
const (
	// maxProcessLine bounds a single stdout frame. A longer line is a protocol
	// violation, surfaced as a read error rather than an unbounded allocation.
	maxProcessLine = 1 << 20
	// maxStderrBytes bounds the swallowed stderr ring.
	maxStderrBytes = 16 * 1024
	// closeGrace is how long Close waits for a clean exit after closing stdin
	// before killing the process.
	closeGrace = 3 * time.Second
)

// ringWriter keeps only the most recent maxStderrBytes written to it and never
// exposes them. It is the sink for a harness's stderr, which is swallowed.
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *ringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

// Process is one launched native harness subprocess. A driver writes one turn
// input line to stdin, reads stdout frames until a terminal, then closes it.
// Its stderr is drained but never forwarded.
type Process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	scan   *bufio.Scanner
	closer sync.Once
}

// VerifyExecutable reports whether exe can be driven at all by running
// `exe --version` and checking it exits 0. It never mutates state.
func VerifyExecutable(ctx context.Context, exe string) error {
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// StartProcess launches exe with args, its working directory set to workspace
// and its environment inherited from the current process (no secrets are
// added). Stderr is swallowed into a bounded ring; stdout is scanned as bounded
// newline frames.
func StartProcess(ctx context.Context, exe, workspace string, args ...string) (*Process, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = workspace
	cmd.Env = os.Environ()
	cmd.Stderr = &ringWriter{max: maxStderrBytes}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 0, 64*1024), maxProcessLine)

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Process{cmd: cmd, stdin: stdin, scan: scan}, nil
}

// WriteLine writes b followed by a newline to the process's stdin as a single
// write.
func (p *Process) WriteLine(b []byte) error {
	buf := make([]byte, 0, len(b)+1)
	buf = append(buf, b...)
	buf = append(buf, '\n')
	_, err := p.stdin.Write(buf)
	return err
}

// ReadLine returns the next stdout frame without its newline, io.EOF when the
// process's stdout closes, or an error (including a bound violation). The
// returned bytes are only valid until the next ReadLine, so callers parse them
// immediately.
func (p *Process) ReadLine() ([]byte, error) {
	if p.scan.Scan() {
		return p.scan.Bytes(), nil
	}
	if err := p.scan.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// Close closes stdin, waits a short grace for a clean exit, then kills the
// process. It is idempotent and always called once after the turn returns.
func (p *Process) Close() error {
	p.closer.Do(func() {
		_ = p.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(closeGrace):
			_ = p.cmd.Process.Kill()
			<-done
		}
	})
	return nil
}

// Abort kills the process immediately. It is safe to call from another
// goroutine and safe to call more than once.
func (p *Process) Abort() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
