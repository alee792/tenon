// Package claude drives Claude Code as a headless turn harness behind the
// harness.Driver seam. Each opened session launches one `claude` process in
// stream-json mode, runs exactly one turn over its stdin/stdout, and reads the
// documented frames to a terminal. Only the four seam events (session
// started/resumed and agent output deltas) cross the boundary; the reason a
// turn failed is a bounded classifier, never model text.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/alee792/tenon/internal/harness"
)

// Driver opens Claude Code sessions. exe is the resolved executable name.
type Driver struct {
	exe string
}

// NewDriver constructs a Claude driver for the given executable, defaulting to
// "claude".
func NewDriver(exe string) Driver {
	if exe == "" {
		exe = "claude"
	}
	return Driver{exe: exe}
}

// Name reports the stable harness name.
func (Driver) Name() string { return "claude" }

// Verify reports whether the claude executable resolves and runs.
func (d Driver) Verify(ctx context.Context) error {
	return harness.VerifyExecutable(ctx, d.exe)
}

// Open launches one claude process for a single turn, resuming the recorded
// native session unless the turn is fresh.
func (d Driver) Open(ctx context.Context, req harness.OpenRequest) (harness.Session, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	resumeID := ""
	if req.ResumeID != "" && !req.Fresh {
		resumeID = req.ResumeID
		args = append(args, "--resume", resumeID)
	}
	proc, err := harness.StartProcess(ctx, d.exe, req.Workspace, args...)
	if err != nil {
		return nil, err
	}
	return &session{proc: proc, resumeID: resumeID}, nil
}

// session is one claude process driven for exactly one turn.
type session struct {
	proc     *harness.Process
	resumeID string
}

// turnInput is one turn's stdin line. Its shape is exact: content is a plain
// string and parent_tool_use_id is always null.
type turnInput struct {
	Type            string      `json:"type"`
	Message         turnMessage `json:"message"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
}

type turnMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// frame is one decoded stdout line. Unused fields for a given type stay zero.
type frame struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
	Message   message `json:"message"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// RunTurn writes the one turn input line, then reads frames until the terminal
// "result". It emits the session event on init and one output delta per
// assistant text block. Any read failure before the result is a process error.
func (s *session) RunTurn(ctx context.Context, in harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	line, err := json.Marshal(turnInput{
		Type:    "user",
		Message: turnMessage{Role: "user", Content: in.Text},
	})
	if err != nil {
		return harness.TurnResult{}, fmt.Errorf("claude: encoding turn input: %w", err)
	}
	if err := s.proc.WriteLine(line); err != nil {
		return harness.TurnResult{}, fmt.Errorf("claude: writing turn input: %w", err)
	}

	for {
		raw, err := s.proc.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return harness.TurnResult{}, errors.New("claude exited before the turn completed")
			}
			return harness.TurnResult{}, fmt.Errorf("claude: reading turn output: %w", err)
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			// A frame we cannot parse (for example the user echo, whose content
			// is a string rather than blocks) carries no terminal or output we
			// act on; skip it rather than fail the turn.
			continue
		}
		switch f.Type {
		case "system":
			if f.Subtype == "init" {
				ev, err := s.initEvent(f)
				if err != nil {
					return harness.TurnResult{}, err
				}
				emit(ev)
			}
		case "assistant":
			for _, delta := range assistantDeltas(f) {
				emit(harness.Event{Type: harness.EventAgentOutputDelta, Delta: delta})
			}
		case "result":
			return classifyResult(f)
		}
		// rate_limit_event and system/post_turn_summary are ignored.
	}
}

// initEvent turns an init frame into the session started/resumed event, failing
// the turn if a resumed session id does not match the one requested.
func (s *session) initEvent(f frame) (harness.Event, error) {
	if s.resumeID != "" {
		if f.SessionID != s.resumeID {
			return harness.Event{}, errors.New("claude resumed an unexpected session")
		}
		return harness.Event{Type: harness.EventSessionResumed, SessionID: f.SessionID}, nil
	}
	return harness.Event{Type: harness.EventSessionStarted, SessionID: f.SessionID}, nil
}

// assistantDeltas extracts the text blocks of an assistant frame in order,
// dropping non-text and empty blocks.
func assistantDeltas(f frame) []string {
	var out []string
	for _, b := range f.Message.Content {
		if b.Type == "text" && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	return out
}

// classifyResult maps a terminal result frame onto a TurnResult. It requires a
// resumable session id and reduces the outcome to a bounded, model-text-free
// classifier: never the "result" text.
func classifyResult(f frame) (harness.TurnResult, error) {
	if f.SessionID == "" {
		return harness.TurnResult{}, errors.New("claude did not provide a resumable session id")
	}
	if !f.IsError && f.Subtype == "success" {
		return harness.TurnResult{SessionID: f.SessionID, Status: harness.StatusCompleted}, nil
	}
	return harness.TurnResult{SessionID: f.SessionID, Status: harness.StatusFailed, Reason: "turn_failed"}, nil
}

// Close releases the process.
func (s *session) Close() error { return s.proc.Close() }

// Abort kills the process, interrupting an in-flight RunTurn.
func (s *session) Abort() { s.proc.Abort() }
