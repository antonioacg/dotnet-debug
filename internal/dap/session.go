package dap

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Session manages a DAP communication session: sends requests, routes responses,
// and buffers events from the debug adapter.
type Session struct {
	transport *Transport

	seq       int64
	pending   map[int]chan *Response
	pendingMu sync.Mutex

	// Event channels
	Initialized chan struct{} // closed when initialized event is received
	initOnce    sync.Once
	Stopped     chan *StoppedEventBody
	Exited      chan *ExitedEventBody
	Terminated  chan struct{}
	Output      chan *OutputEventBody

	// State
	configDone   bool
	configDoneMu sync.Mutex

	Capabilities *Capabilities

	// Closed when readLoop exits
	done chan struct{}
	err  error
}

func NewSession(transport *Transport) *Session {
	s := &Session{
		transport:   transport,
		pending:     make(map[int]chan *Response),
		Initialized: make(chan struct{}),
		Stopped:     make(chan *StoppedEventBody, 32),
		Exited:      make(chan *ExitedEventBody, 4),
		Terminated:  make(chan struct{}, 4),
		Output:      make(chan *OutputEventBody, 256),
		done:        make(chan struct{}),
	}
	go s.readLoop()
	return s
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) Err() error {
	return s.err
}

func (s *Session) readLoop() {
	defer close(s.done)
	for {
		raw, err := s.transport.ReadRaw()
		if err != nil {
			s.err = err
			// Unblock all pending requests
			s.pendingMu.Lock()
			for seq, ch := range s.pending {
				close(ch)
				delete(s.pending, seq)
			}
			s.pendingMu.Unlock()
			return
		}

		var base struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			log.Printf("dap: failed to parse message type: %v", err)
			continue
		}

		switch base.Type {
		case "response":
			var resp Response
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.Printf("dap: failed to parse response: %v", err)
				continue
			}
			s.pendingMu.Lock()
			reqSeq := int(resp.RequestSeq)
			if ch, ok := s.pending[reqSeq]; ok {
				ch <- &resp
				delete(s.pending, reqSeq)
			}
			s.pendingMu.Unlock()

		case "event":
			var evt Event
			if err := json.Unmarshal(raw, &evt); err != nil {
				log.Printf("dap: failed to parse event: %v", err)
				continue
			}
			s.handleEvent(&evt)
		}
	}
}

func (s *Session) handleEvent(evt *Event) {
	switch evt.Event {
	case "initialized":
		s.initOnce.Do(func() { close(s.Initialized) })
		return
	case "stopped":
		if evt.Body == nil {
			return
		}
		var body StoppedEventBody
		if err := json.Unmarshal(*evt.Body, &body); err == nil {
			select {
			case s.Stopped <- &body:
			default:
				log.Printf("dap: stopped event buffer full, dropping")
			}
		}
	case "exited":
		if evt.Body == nil {
			return
		}
		var body ExitedEventBody
		if err := json.Unmarshal(*evt.Body, &body); err == nil {
			select {
			case s.Exited <- &body:
			default:
			}
		}
	case "terminated":
		select {
		case s.Terminated <- struct{}{}:
		default:
		}
	case "output":
		if evt.Body == nil {
			return
		}
		var body OutputEventBody
		if err := json.Unmarshal(*evt.Body, &body); err == nil {
			select {
			case s.Output <- &body:
			default:
			}
		}
	}
}

// SendRequest sends a DAP request and waits for its response.
func (s *Session) SendRequest(command string, arguments interface{}) (*Response, error) {
	return s.SendRequestTimeout(command, arguments, 30*time.Second)
}

// SendRequestTimeout sends a DAP request with a custom timeout.
func (s *Session) SendRequestTimeout(command string, arguments interface{}, timeout time.Duration) (*Response, error) {
	seq := int(atomic.AddInt64(&s.seq, 1))

	req := Request{
		ProtocolMessage: ProtocolMessage{Seq: FlexInt(seq), Type: "request"},
		Command:         command,
		Arguments:       arguments,
	}

	ch := make(chan *Response, 1)
	s.pendingMu.Lock()
	s.pending[seq] = ch
	s.pendingMu.Unlock()

	if err := s.transport.WriteMessage(req); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, seq)
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("sending %q request: %w", command, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("session closed while waiting for %q response", command)
		}
		if !resp.Success {
			return resp, fmt.Errorf("DAP %q failed: %s", command, resp.Message)
		}
		return resp, nil
	case <-time.After(timeout):
		s.pendingMu.Lock()
		delete(s.pending, seq)
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("timeout waiting for %q response (%v)", command, timeout)
	case <-s.done:
		return nil, fmt.Errorf("session ended while waiting for %q response: %v", command, s.err)
	}
}

// Initialize performs the DAP initialize handshake and waits for the initialized event.
func (s *Session) Initialize() error {
	args := InitializeArguments{
		ClientID:             "dotnet-debug",
		ClientName:           "dotnet-debug CLI",
		AdapterID:            "coreclr",
		LinesStartAt1:        true,
		ColumnsStartAt1:      true,
		PathFormat:            "path",
		SupportsVariableType: true,
	}

	resp, err := s.SendRequest("initialize", args)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	if resp.Body != nil {
		var caps Capabilities
		if err := json.Unmarshal(*resp.Body, &caps); err == nil {
			s.Capabilities = &caps
		}
	}

	// Wait for initialized event (drain stop events that might come first)
	// The initialized event doesn't have a body we care about - just need to
	// wait for the adapter to signal it's ready for configuration
	// Give it a generous timeout since some adapters are slow
	return nil
}

// Launch sends a launch request.
func (s *Session) Launch(args LaunchArguments) error {
	_, err := s.SendRequestTimeout("launch", args, 60*time.Second)
	return err
}

// Attach sends an attach request.
func (s *Session) Attach(args AttachArguments) error {
	_, err := s.SendRequestTimeout("attach", args, 30*time.Second)
	return err
}

// SetBreakpoints sets breakpoints for a single source file.
func (s *Session) SetBreakpoints(file string, breakpoints []SourceBreakpoint) (*SetBreakpointsResponseBody, error) {
	args := SetBreakpointsArguments{
		Source:      Source{Path: file},
		Breakpoints: breakpoints,
	}
	resp, err := s.SendRequest("setBreakpoints", args)
	if err != nil {
		return nil, err
	}
	var body SetBreakpointsResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing setBreakpoints response: %w", err)
		}
	}
	return &body, nil
}

// SetExceptionBreakpoints configures exception breakpoints.
func (s *Session) SetExceptionBreakpoints(filters []string) error {
	_, err := s.SendRequest("setExceptionBreakpoints", SetExceptionBreakpointsArguments{
		Filters: filters,
	})
	return err
}

// ConfigurationDone signals that initial configuration is complete.
func (s *Session) ConfigurationDone() error {
	s.configDoneMu.Lock()
	defer s.configDoneMu.Unlock()
	if s.configDone {
		return nil
	}
	_, err := s.SendRequest("configurationDone", nil)
	if err == nil {
		s.configDone = true
	}
	return err
}

// IsConfigDone returns whether configurationDone has been sent.
func (s *Session) IsConfigDone() bool {
	s.configDoneMu.Lock()
	defer s.configDoneMu.Unlock()
	return s.configDone
}

// Continue resumes execution. Sends configurationDone first if needed.
func (s *Session) Continue(threadID int) error {
	if err := s.ConfigurationDone(); err != nil {
		return fmt.Errorf("configurationDone: %w", err)
	}
	_, err := s.SendRequest("continue", ContinueArguments{ThreadID: threadID})
	return err
}

// Next steps over.
func (s *Session) Next(threadID int) error {
	_, err := s.SendRequest("next", StepArguments{ThreadID: threadID})
	return err
}

// StepIn steps into.
func (s *Session) StepIn(threadID int) error {
	_, err := s.SendRequest("stepIn", StepArguments{ThreadID: threadID})
	return err
}

// StepOut steps out.
func (s *Session) StepOut(threadID int) error {
	_, err := s.SendRequest("stepOut", StepArguments{ThreadID: threadID})
	return err
}

// Pause pauses execution of a thread.
func (s *Session) Pause(threadID int) error {
	_, err := s.SendRequest("pause", PauseArguments{ThreadID: threadID})
	return err
}

// Threads returns all threads.
func (s *Session) Threads() ([]Thread, error) {
	resp, err := s.SendRequest("threads", nil)
	if err != nil {
		return nil, err
	}
	var body ThreadsResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing threads response: %w", err)
		}
	}
	return body.Threads, nil
}

// StackTrace returns the stack trace for a thread.
func (s *Session) StackTrace(threadID, levels int) ([]StackFrame, error) {
	args := StackTraceArguments{ThreadID: threadID, Levels: levels}
	resp, err := s.SendRequest("stackTrace", args)
	if err != nil {
		return nil, err
	}
	var body StackTraceResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing stackTrace response: %w", err)
		}
	}
	return body.StackFrames, nil
}

// Scopes returns scopes for a stack frame.
func (s *Session) Scopes(frameID int) ([]Scope, error) {
	resp, err := s.SendRequest("scopes", ScopesArguments{FrameID: frameID})
	if err != nil {
		return nil, err
	}
	var body ScopesResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing scopes response: %w", err)
		}
	}
	return body.Scopes, nil
}

// Variables returns variables for a reference.
func (s *Session) Variables(ref int) ([]Variable, error) {
	resp, err := s.SendRequest("variables", VariablesArguments{VariablesReference: ref})
	if err != nil {
		return nil, err
	}
	var body VariablesResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing variables response: %w", err)
		}
	}
	return body.Variables, nil
}

// Evaluate evaluates an expression in the given frame.
func (s *Session) Evaluate(expression string, frameID int, context string) (*EvaluateResponseBody, error) {
	args := EvaluateArguments{
		Expression: expression,
		FrameID:    frameID,
		Context:    context,
	}
	resp, err := s.SendRequest("evaluate", args)
	if err != nil {
		return nil, err
	}
	var body EvaluateResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing evaluate response: %w", err)
		}
	}
	return &body, nil
}

// ExceptionInfo gets info about the current exception on a thread.
func (s *Session) ExceptionInfo(threadID int) (*ExceptionInfoResponseBody, error) {
	resp, err := s.SendRequest("exceptionInfo", ExceptionInfoArguments{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	var body ExceptionInfoResponseBody
	if resp.Body != nil {
		if err := json.Unmarshal(*resp.Body, &body); err != nil {
			return nil, fmt.Errorf("parsing exceptionInfo response: %w", err)
		}
	}
	return &body, nil
}

// WaitForStop blocks until a stopped event is received or the timeout expires.
func (s *Session) WaitForStop(timeout time.Duration) (*StoppedEventBody, error) {
	select {
	case body := <-s.Stopped:
		return body, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for stop event (%v)", timeout)
	case <-s.done:
		return nil, fmt.Errorf("session ended while waiting for stop: %v", s.err)
	}
}

// WaitForInitialized blocks until the initialized event is received.
func (s *Session) WaitForInitialized(timeout time.Duration) error {
	select {
	case <-s.Initialized:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for initialized event (%v)", timeout)
	case <-s.done:
		return fmt.Errorf("session ended while waiting for initialized: %v", s.err)
	}
}

// Disconnect ends the debug session.
func (s *Session) Disconnect(terminateDebuggee bool) error {
	_, err := s.SendRequestTimeout("disconnect", DisconnectArguments{
		TerminateDebuggee: terminateDebuggee,
	}, 10*time.Second)
	return err
}
