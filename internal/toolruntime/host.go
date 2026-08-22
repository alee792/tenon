package toolruntime

// The host protocol is tenon's own line-delimited JSON, not MCP: tenon owns
// the MCP boundary and speaks this simpler protocol to the language hosts it
// launched itself. One request object per line goes to the host's stdin and
// one response object per line comes back on its stdout:
//
//	{"id":"go-1","method":"list","params":null}
//	{"id":"go-1","result":{"instanceId":"go:4711","tools":[...]}}
//	{"id":"go-2","method":"call","params":{"name":"hash-text","arguments":{"text":"hi"}}}
//	{"id":"go-2","result":{"instanceId":"go:4711","output":{"hex":"..."}}}
//	{"id":"go-3","error":"arguments is missing the required field text"}
//
// Every bound is enforced on this side. A host that answers with an oversized
// line, a mismatched id, a mismatched instance, or anything unparseable has
// left the protocol, so it is killed and its error is a bounded sentence of
// tenon's own words. Host stderr is drained into a bounded ring and never
// forwarded: a diagnostic must never carry raw process output.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// MaxCallLineBytes bounds one call response line.
	MaxCallLineBytes = 64 << 10
	// MaxCatalogLineBytes bounds one list response line. A catalog carries
	// every tool's two JSON Schemas, so it is bounded far higher than a call.
	MaxCatalogLineBytes = 8 << 20
	// MaxArgumentBytes bounds the arguments of one call and the output of
	// one call.
	MaxArgumentBytes = 64 << 10
	// maxStderrBytes bounds the retained tail of one host's stderr.
	maxStderrBytes = 16 << 10
	// closeGrace is how long a host has to exit after its stdin closes
	// before it is killed.
	closeGrace = 2 * time.Second
)

// errLineTooLong marks a response line over its bound. The line is neither
// truncated nor partially interpreted.
var errLineTooLong = errors.New("line exceeded its bound")

// Definition is one authored tool exactly as its language host reports it.
// The name, description, and both schemas are published verbatim on the
// managed MCP surface.
type Definition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	// Language is the host that reported the tool. It is set by tenon, not
	// by the host.
	Language string `json:"-"`
}

// host is one long-lived language host process. Calls are serialized: a host
// serves one call at a time, so authored code never sees interleaved requests
// and a deadline always names exactly one call.
type host struct {
	language string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	stderr   *ring

	calls sync.Mutex
	seq   int
	// instance is the host's reported instance identifier, learned from its
	// first response and required to stay the same for the session.
	instance string

	state  sync.Mutex
	failed error
	closed bool
}

// startHost launches one language host. cmd carries its own environment,
// working directory, and arguments; this function owns only the pipes.
func startHost(language string, cmd *exec.Cmd) (*host, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("the %s language host could not be started", language)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("the %s language host could not be started", language)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("the %s language host could not be started", language)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the %s language host could not be started", language)
	}
	h := &host{
		language: language,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReaderSize(stdout, 64<<10),
		stderr:   &ring{},
	}
	// Draining stderr is not optional: a host that fills its pipe would
	// block forever. The bytes are bounded and never leave this process.
	go func() { _, _ = io.Copy(h.stderr, stderr) }()
	return h, nil
}

// catalog issues one list request and returns the reported definitions.
func (h *host) catalog(timeout time.Duration) ([]Definition, error) {
	raw, err := h.exchange("list", nil, MaxCatalogLineBytes, timeout)
	if err != nil {
		return nil, err
	}
	var result struct {
		InstanceID string       `json:"instanceId"`
		Tools      []Definition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, h.terminate(h.protocolError("returned an unparseable catalog"))
	}
	if err := h.bindInstance(result.InstanceID); err != nil {
		return nil, err
	}
	for i := range result.Tools {
		result.Tools[i].Language = h.language
	}
	return result.Tools, nil
}

// invoke issues one call request and returns the tool's output. The deadline
// is tenon's, not the host's: a host that overruns it is terminated, because a
// language host has no obligation to be interruptible.
func (h *host) invoke(name string, arguments json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	if err := boundedJSON(arguments, "the tool arguments"); err != nil {
		return nil, err
	}
	params := map[string]any{"name": name, "arguments": arguments}
	raw, err := h.exchange("call", params, MaxCallLineBytes, timeout)
	if err != nil {
		return nil, err
	}
	var result struct {
		InstanceID string          `json:"instanceId"`
		Output     json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, h.terminate(h.protocolError("returned an unparseable call result"))
	}
	if err := h.bindInstance(result.InstanceID); err != nil {
		return nil, err
	}
	if err := boundedJSON(result.Output, "the tool output"); err != nil {
		return nil, err
	}
	return result.Output, nil
}

// exchange writes one request and reads exactly one response line.
func (h *host) exchange(method string, params any, limit int, timeout time.Duration) (json.RawMessage, error) {
	h.calls.Lock()
	defer h.calls.Unlock()
	if err := h.failure(); err != nil {
		return nil, err
	}

	h.seq++
	id := fmt.Sprintf("%s-%d", h.language, h.seq)
	line, err := json.Marshal(struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("the %s tool request could not be encoded", h.language)
	}

	// The deadline is armed around the whole exchange. On expiry the host is
	// killed, which unblocks the read below with a closed pipe.
	expired := time.AfterFunc(timeout, func() {
		_ = h.terminate(errors.New("tool call exceeded its deadline; language host terminated"))
	})
	defer expired.Stop()

	if _, err := h.stdin.Write(append(line, '\n')); err != nil {
		return nil, h.terminate(h.protocolError("stopped accepting requests"))
	}
	raw, err := readLine(h.stdout, limit)
	if errors.Is(err, errLineTooLong) {
		return nil, h.terminate(fmt.Errorf(
			"the %s language host returned a response line over its bound of %d bytes; the host was terminated",
			h.language, limit))
	}
	if err != nil {
		return nil, h.terminate(h.protocolError("stopped responding"))
	}

	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, h.terminate(h.protocolError("returned an unparseable response line"))
	}
	if response.ID != id {
		return nil, h.terminate(h.protocolError("answered with a mismatched request identifier"))
	}
	if response.Error != "" {
		// The host's own bounded sentence about authored code, not process
		// output: it is safe to report and is already the host's own words.
		return nil, fmt.Errorf("%s", bound(response.Error))
	}
	if len(response.Result) == 0 {
		return nil, h.terminate(h.protocolError("answered with neither a result nor an error"))
	}
	return response.Result, nil
}

// bindInstance pins the host's reported instance for the whole session. One
// host serves every call, so a changed instance means the process behind the
// pipe is not the one tenon inspected.
func (h *host) bindInstance(instance string) error {
	if instance == "" {
		return h.terminate(h.protocolError("reported no instance identifier"))
	}
	h.state.Lock()
	if h.instance == "" {
		h.instance = instance
	}
	same := h.instance == instance
	h.state.Unlock()
	if !same {
		return h.terminate(h.protocolError("reported a different instance mid-session"))
	}
	return nil
}

func (h *host) protocolError(what string) error {
	return fmt.Errorf("the %s language host %s; the host was terminated", h.language, what)
}

// terminate records the first failure, kills the process, and returns the
// recorded failure so every later call reports the same cause.
func (h *host) terminate(cause error) error {
	h.state.Lock()
	if h.failed == nil {
		h.failed = cause
	}
	recorded := h.failed
	h.state.Unlock()

	_ = h.stdin.Close()
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	return recorded
}

func (h *host) failure() error {
	h.state.Lock()
	defer h.state.Unlock()
	return h.failed
}

// Close ends the session: stdin closes so a well-behaved host exits on its
// own, and a host still running after the grace period is killed.
func (h *host) close() {
	h.state.Lock()
	if h.closed {
		h.state.Unlock()
		return
	}
	h.closed = true
	h.state.Unlock()

	_ = h.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = h.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeGrace):
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		<-done
	}
}

// readLine returns one line without its terminator, refusing a line over
// limit rather than truncating or partially interpreting it.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errLineTooLong
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return bytes.TrimRight(line, "\r\n"), nil
			}
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

// boundedJSON refuses arguments or output that are absent, oversized, or not
// valid JSON before they cross the boundary in either direction.
func boundedJSON(raw json.RawMessage, surface string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%s must be one non-empty JSON value", surface)
	}
	if len(raw) > MaxArgumentBytes {
		return fmt.Errorf("%s may contain at most %d bytes", surface, MaxArgumentBytes)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%s must be valid JSON", surface)
	}
	return nil
}

// ring retains the tail of a host's stderr in bounded memory. Nothing reads it
// but tenon's own tests: host stderr is never forwarded into a diagnostic, a
// tool result, or an audit line.
type ring struct {
	mu     sync.Mutex
	buffer []byte
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buffer = append(r.buffer, p...)
	if len(r.buffer) > maxStderrBytes {
		r.buffer = append([]byte(nil), r.buffer[len(r.buffer)-maxStderrBytes:]...)
	}
	return len(p), nil
}

func (r *ring) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buffer)
}

// bound trims a host-supplied sentence to one bounded line.
func bound(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) > maxMessageChars {
		return flat[:maxMessageChars] + "..."
	}
	return flat
}

// maxMessageChars bounds one host-supplied message as tenon reports it.
const maxMessageChars = 512
