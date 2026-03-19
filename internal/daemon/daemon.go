package daemon

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"dotnet-debug/internal/dap"
	"dotnet-debug/internal/paths"
	"dotnet-debug/internal/proto"
)

const inactivityTimeout = 2 * time.Hour

// Daemon manages a netcoredbg subprocess and accepts CLI commands over TCP.
type Daemon struct {
	config  proto.DaemonConfig
	session *dap.Session
	cmd     *exec.Cmd
	token   string
	port    int

	mu       sync.Mutex
	state    string // "running", "stopped", "exited"
	stopInfo *proto.StopInfo
	output   []string // ring buffer of recent output lines
	outputMu sync.Mutex

	// stopCh distributes stop events from monitorEvents to command handlers.
	// monitorEvents is the sole consumer of session.Stopped.
	stopCh chan *proto.StopInfo

	lastActivity time.Time
	activityMu   sync.Mutex

	listener net.Listener
	quit     chan struct{}
}

// Run is the daemon's main entry point. It blocks until the session ends or is stopped.
func Run(configJSON string) error {
	var config proto.DaemonConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return fmt.Errorf("parsing daemon config: %w", err)
	}

	d := &Daemon{
		config: config,
		state:  "running",
		stopCh: make(chan *proto.StopInfo, 1),
		quit:   make(chan struct{}),
	}

	// Ignore SIGINT so Ctrl+C on the CLI doesn't kill the daemon
	signal.Ignore(os.Interrupt)

	if err := d.start(); err != nil {
		d.cleanup()
		return err
	}

	d.serve()
	d.cleanup()
	return nil
}

func (d *Daemon) start() error {
	// Generate auth token
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generating token: %w", err)
	}
	d.token = hex.EncodeToString(tokenBytes)

	// Start TCP listener on random port
	var err error
	d.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("starting TCP listener: %w", err)
	}
	d.port = d.listener.Addr().(*net.TCPAddr).Port
	log.Printf("daemon: listening on 127.0.0.1:%d", d.port)

	// Start netcoredbg
	if err := d.startNetcoredbg(); err != nil {
		return fmt.Errorf("starting netcoredbg: %w", err)
	}

	// Initialize DAP session
	if err := d.session.Initialize(); err != nil {
		return fmt.Errorf("DAP initialize: %w", err)
	}
	if err := d.session.WaitForInitialized(30 * time.Second); err != nil {
		return fmt.Errorf("waiting for initialized: %w", err)
	}

	// Launch or attach
	switch d.config.Mode {
	case "launch":
		args := dap.LaunchArguments{
			Program:     d.config.Program,
			Args:        d.config.Args,
			Cwd:         d.config.Cwd,
			Env:         d.config.Env,
			StopAtEntry: d.config.StopAtEntry,
			JustMyCode:  true,
		}
		if args.Cwd == "" {
			args.Cwd = filepath.Dir(d.config.Program)
		}
		if err := d.session.Launch(args); err != nil {
			return fmt.Errorf("DAP launch: %w", err)
		}
	case "attach":
		if err := d.session.Attach(dap.AttachArguments{ProcessID: d.config.PID}); err != nil {
			return fmt.Errorf("DAP attach: %w", err)
		}
	default:
		return fmt.Errorf("unknown mode: %s", d.config.Mode)
	}

	// Write session file
	d.lastActivity = time.Now()
	sf := proto.SessionFile{
		ID:           d.config.SessionID,
		Port:         d.port,
		DaemonPID:    os.Getpid(),
		Token:        d.token,
		Program:      d.config.Program,
		AttachedPID:  d.config.PID,
		Created:      d.lastActivity.Format(time.RFC3339),
		LastActivity: d.lastActivity.Format(time.RFC3339),
	}
	if err := writeSessionFile(sf); err != nil {
		return fmt.Errorf("writing session file: %w", err)
	}
	log.Printf("daemon: session %q ready", d.config.SessionID)

	// Start event monitor goroutine
	go d.monitorEvents()

	return nil
}

func (d *Daemon) startNetcoredbg() error {
	d.cmd = exec.Command(d.config.NetcoredbgPath, "--interpreter=vscode")

	stdin, err := d.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := d.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	d.cmd.Stderr = os.Stderr // daemon's stderr goes to log file

	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("starting netcoredbg: %w", err)
	}
	log.Printf("daemon: netcoredbg started (PID %d)", d.cmd.Process.Pid)

	transport := dap.NewTransport(stdout, stdin)
	d.session = dap.NewSession(transport)

	// Monitor netcoredbg process exit
	go func() {
		if err := d.cmd.Wait(); err != nil {
			log.Printf("daemon: netcoredbg exited: %v", err)
		} else {
			log.Printf("daemon: netcoredbg exited normally")
		}
	}()

	return nil
}

func (d *Daemon) monitorEvents() {
	for {
		select {
		case body := <-d.session.Stopped:
			info := proto.StopInfo{
				Reason:      body.Reason,
				Description: body.Description,
				ThreadID:    body.ThreadID,
			}
			// Resolve file/line from stack trace
			if frames, err := d.session.StackTrace(body.ThreadID, 1); err == nil && len(frames) > 0 {
				if frames[0].Source != nil {
					info.File = frames[0].Source.Path
				}
				info.Line = frames[0].Line
				info.Column = frames[0].Column
			}

			d.mu.Lock()
			d.state = "stopped"
			d.stopInfo = &info
			d.mu.Unlock()
			log.Printf("daemon: stopped (reason=%s, thread=%d, %s:%d)", info.Reason, info.ThreadID, info.File, info.Line)

			// Signal any waiter (non-blocking)
			select {
			case d.stopCh <- &info:
			default:
			}

		case body := <-d.session.Output:
			if body.Category == "stdout" || body.Category == "console" || body.Category == "" {
				d.outputMu.Lock()
				d.output = append(d.output, body.Output)
				if len(d.output) > 1000 {
					d.output = d.output[len(d.output)-500:]
				}
				d.outputMu.Unlock()
			}

		case <-d.session.Terminated:
			d.mu.Lock()
			d.state = "exited"
			d.mu.Unlock()
			log.Printf("daemon: debuggee terminated")

		case body := <-d.session.Exited:
			d.mu.Lock()
			d.state = "exited"
			d.mu.Unlock()
			log.Printf("daemon: debuggee exited (code=%d)", body.ExitCode)

		case <-d.session.Done():
			log.Printf("daemon: DAP session ended: %v", d.session.Err())
			close(d.quit)
			return

		case <-d.quit:
			return
		}
	}
}

func (d *Daemon) serve() {
	inactivity := time.NewTimer(inactivityTimeout)
	defer inactivity.Stop()

	connCh := make(chan net.Conn)
	go func() {
		for {
			conn, err := d.listener.Accept()
			if err != nil {
				select {
				case <-d.quit:
					return
				default:
					log.Printf("daemon: accept error: %v", err)
					continue
				}
			}
			connCh <- conn
		}
	}()

	for {
		select {
		case conn := <-connCh:
			d.handleConnection(conn)
			d.touchActivity()
			inactivity.Reset(inactivityTimeout)

		case <-inactivity.C:
			log.Printf("daemon: inactivity timeout (%v), shutting down", inactivityTimeout)
			d.shutdownSession()
			return

		case <-d.quit:
			return
		}
	}
}

func (d *Daemon) handleConnection(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Minute))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		log.Printf("daemon: read error: %v", err)
		return
	}

	var cmd proto.Command
	if err := json.Unmarshal(line, &cmd); err != nil {
		writeResult(conn, proto.Result{Error: "invalid JSON: " + err.Error()})
		return
	}

	if cmd.Token != d.token {
		writeResult(conn, proto.Result{Error: "invalid token"})
		return
	}

	result := d.dispatch(cmd)
	writeResult(conn, result)
}

func (d *Daemon) dispatch(cmd proto.Command) proto.Result {
	switch cmd.Cmd {
	case "breakpoints":
		return d.cmdBreakpoints(cmd.Args)
	case "exception-breakpoints":
		return d.cmdExceptionBreakpoints(cmd.Args)
	case "continue":
		return d.cmdContinue(cmd.Args)
	case "next":
		return d.cmdStep("next", cmd.Args)
	case "step-in":
		return d.cmdStep("stepIn", cmd.Args)
	case "step-out":
		return d.cmdStep("stepOut", cmd.Args)
	case "pause":
		return d.cmdPause(cmd.Args)
	case "wait":
		return d.cmdWait(cmd.Args)
	case "inspect":
		return d.cmdInspect(cmd.Args)
	case "eval":
		return d.cmdEval(cmd.Args)
	case "threads":
		return d.cmdThreads()
	case "stack":
		return d.cmdStack(cmd.Args)
	case "scopes":
		return d.cmdScopes(cmd.Args)
	case "variables":
		return d.cmdVariables(cmd.Args)
	case "output":
		return d.cmdOutput(cmd.Args)
	case "status":
		return d.cmdStatus()
	case "disconnect":
		return d.cmdDisconnect(cmd.Args)
	default:
		return proto.Result{Error: fmt.Sprintf("unknown command: %s", cmd.Cmd)}
	}
}

// --- Command handlers ---

func (d *Daemon) cmdBreakpoints(raw json.RawMessage) proto.Result {
	var args proto.BreakpointArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return proto.Result{Error: "invalid args: " + err.Error()}
	}

	bps := make([]dap.SourceBreakpoint, len(args.Lines))
	for i, line := range args.Lines {
		bp := dap.SourceBreakpoint{Line: line}
		if i < len(args.Conditions) && args.Conditions[i] != "" {
			bp.Condition = args.Conditions[i]
		}
		bps[i] = bp
	}

	resp, err := d.session.SetBreakpoints(args.File, bps)
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	infos := make([]proto.BreakpointInfo, len(resp.Breakpoints))
	for i, bp := range resp.Breakpoints {
		infos[i] = proto.BreakpointInfo{
			ID:       bp.ID,
			Verified: bp.Verified,
			Line:     bp.Line,
			File:     args.File,
			Message:  bp.Message,
		}
	}
	return proto.Result{OK: true, Data: infos}
}

func (d *Daemon) cmdExceptionBreakpoints(raw json.RawMessage) proto.Result {
	var args proto.ExceptionBreakpointArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return proto.Result{Error: "invalid args: " + err.Error()}
	}
	if err := d.session.SetExceptionBreakpoints(args.Filters); err != nil {
		return proto.Result{Error: err.Error()}
	}
	return proto.Result{OK: true, Data: "exception breakpoints set"}
}

func (d *Daemon) cmdContinue(raw json.RawMessage) proto.Result {
	var args proto.ContinueArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	threadID := args.ThreadID
	if threadID == 0 {
		d.mu.Lock()
		if d.stopInfo != nil {
			threadID = d.stopInfo.ThreadID
		}
		d.mu.Unlock()
	}

	d.mu.Lock()
	d.state = "running"
	d.stopInfo = nil
	d.mu.Unlock()
	d.drainStopCh()

	if err := d.session.Continue(threadID); err != nil {
		return proto.Result{Error: err.Error()}
	}

	// Default: wait for stop
	waitForStop := args.WaitForStop != 0 || raw == nil
	if !waitForStop {
		return proto.Result{OK: true, Data: "continued"}
	}

	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return d.waitForStopResult(timeout)
}

func (d *Daemon) cmdStep(kind string, raw json.RawMessage) proto.Result {
	var args proto.StepArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	threadID := args.ThreadID
	if threadID == 0 {
		d.mu.Lock()
		if d.stopInfo != nil {
			threadID = d.stopInfo.ThreadID
		}
		d.mu.Unlock()
	}

	d.mu.Lock()
	d.state = "running"
	d.stopInfo = nil
	d.mu.Unlock()
	d.drainStopCh()

	var err error
	switch kind {
	case "next":
		err = d.session.Next(threadID)
	case "stepIn":
		err = d.session.StepIn(threadID)
	case "stepOut":
		err = d.session.StepOut(threadID)
	}
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return d.waitForStopResult(timeout)
}

func (d *Daemon) cmdPause(raw json.RawMessage) proto.Result {
	var args proto.PauseArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}
	if err := d.session.Pause(args.ThreadID); err != nil {
		return proto.Result{Error: err.Error()}
	}
	return proto.Result{OK: true, Data: "paused"}
}

func (d *Daemon) cmdWait(raw json.RawMessage) proto.Result {
	var args proto.WaitArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	// If already stopped, return immediately
	d.mu.Lock()
	if d.state == "stopped" && d.stopInfo != nil {
		info := *d.stopInfo
		d.mu.Unlock()
		return proto.Result{OK: true, Data: info}
	}
	if d.state == "exited" {
		d.mu.Unlock()
		return proto.Result{OK: true, Data: proto.StopInfo{Reason: "exited"}}
	}
	d.mu.Unlock()

	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	select {
	case info := <-d.stopCh:
		return proto.Result{OK: true, Data: *info}
	case <-time.After(timeout):
		d.mu.Lock()
		if d.state == "stopped" && d.stopInfo != nil {
			info := *d.stopInfo
			d.mu.Unlock()
			return proto.Result{OK: true, Data: info}
		}
		d.mu.Unlock()
		return proto.Result{Error: fmt.Sprintf("timeout waiting for stop event (%v)", timeout)}
	case <-d.quit:
		return proto.Result{Error: "session ended"}
	}
}

// drainStopCh clears any stale stop event. Call BEFORE sending a resume/step command.
func (d *Daemon) drainStopCh() {
	select {
	case <-d.stopCh:
	default:
	}
}

func (d *Daemon) waitForStopResult(timeout time.Duration) proto.Result {
	select {
	case info := <-d.stopCh:
		return proto.Result{OK: true, Data: *info}
	case <-time.After(timeout):
		// Check if we got stopped while setting up the timer
		d.mu.Lock()
		if d.state == "stopped" && d.stopInfo != nil {
			info := *d.stopInfo
			d.mu.Unlock()
			return proto.Result{OK: true, Data: info}
		}
		if d.state == "exited" {
			d.mu.Unlock()
			return proto.Result{OK: true, Data: proto.StopInfo{Reason: "exited"}}
		}
		d.mu.Unlock()
		return proto.Result{Error: fmt.Sprintf("timeout waiting for stop event (%v)", timeout)}
	case <-d.quit:
		return proto.Result{Error: "session ended"}
	}
}

func (d *Daemon) cmdInspect(raw json.RawMessage) proto.Result {
	var args proto.InspectArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	depth := args.Depth
	if depth == 0 {
		depth = 2
	}

	d.mu.Lock()
	state := d.state
	var stopCopy *proto.StopInfo
	if d.stopInfo != nil {
		cp := *d.stopInfo
		stopCopy = &cp
	}
	d.mu.Unlock()

	if state != "stopped" {
		return proto.Result{Error: fmt.Sprintf("cannot inspect: debuggee is %s (not stopped)", state)}
	}

	threadID := args.ThreadID
	if threadID == 0 && stopCopy != nil {
		threadID = stopCopy.ThreadID
	}

	result := proto.InspectResult{Stopped: stopCopy}

	// Threads
	if threads, err := d.session.Threads(); err == nil {
		for _, t := range threads {
			result.Threads = append(result.Threads, proto.ThreadInfo{ID: t.ID, Name: t.Name})
		}
	}

	// Stack trace
	frames, err := d.session.StackTrace(threadID, 20)
	if err == nil {
		for _, f := range frames {
			fi := proto.FrameInfo{ID: f.ID, Name: f.Name, Line: f.Line, Column: f.Column}
			if f.Source != nil {
				fi.File = f.Source.Path
			}
			result.StackTrace = append(result.StackTrace, fi)
		}
	}

	// Scopes + variables for top frame
	if len(frames) > 0 {
		scopes, err := d.session.Scopes(frames[0].ID)
		if err == nil {
			for _, scope := range scopes {
				si := proto.ScopeInfo{Name: scope.Name}
				if vars, err := d.session.Variables(scope.VariablesReference); err == nil {
					si.Variables = d.expandVariables(vars, depth)
				}
				result.Scopes = append(result.Scopes, si)
			}
		}
	}

	// Exception info
	if stopCopy != nil && stopCopy.Reason == "exception" {
		if info, err := d.session.ExceptionInfo(threadID); err == nil {
			result.Exception = &proto.ExceptionInfo{
				ID:          info.ExceptionID,
				Description: info.Description,
			}
			if info.Details != nil {
				result.Exception.StackTrace = info.Details.StackTrace
			}
		}
	}

	return proto.Result{OK: true, Data: result}
}

func (d *Daemon) expandVariables(vars []dap.Variable, depth int) []proto.VariableInfo {
	result := make([]proto.VariableInfo, 0, len(vars))
	for _, v := range vars {
		vi := proto.VariableInfo{
			Name:  v.Name,
			Value: v.Value,
			Type:  v.Type,
		}
		if v.VariablesReference > 0 && depth > 0 {
			if children, err := d.session.Variables(v.VariablesReference); err == nil {
				vi.Children = d.expandVariables(children, depth-1)
			}
		}
		result = append(result, vi)
	}
	return result
}

func (d *Daemon) cmdEval(raw json.RawMessage) proto.Result {
	var args proto.EvalArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return proto.Result{Error: "invalid args: " + err.Error()}
	}

	resp, err := d.session.Evaluate(args.Expression, args.FrameID, "repl")
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	return proto.Result{OK: true, Data: map[string]interface{}{
		"result": resp.Result,
		"type":   resp.Type,
	}}
}

func (d *Daemon) cmdThreads() proto.Result {
	threads, err := d.session.Threads()
	if err != nil {
		return proto.Result{Error: err.Error()}
	}
	infos := make([]proto.ThreadInfo, len(threads))
	for i, t := range threads {
		infos[i] = proto.ThreadInfo{ID: t.ID, Name: t.Name}
	}
	return proto.Result{OK: true, Data: infos}
}

func (d *Daemon) cmdStack(raw json.RawMessage) proto.Result {
	var args proto.StackTraceArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	threadID := args.ThreadID
	if threadID == 0 {
		d.mu.Lock()
		if d.stopInfo != nil {
			threadID = d.stopInfo.ThreadID
		}
		d.mu.Unlock()
	}

	levels := args.Levels
	if levels == 0 {
		levels = 20
	}

	frames, err := d.session.StackTrace(threadID, levels)
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	infos := make([]proto.FrameInfo, len(frames))
	for i, f := range frames {
		infos[i] = proto.FrameInfo{ID: f.ID, Name: f.Name, Line: f.Line, Column: f.Column}
		if f.Source != nil {
			infos[i].File = f.Source.Path
		}
	}
	return proto.Result{OK: true, Data: infos}
}

func (d *Daemon) cmdScopes(raw json.RawMessage) proto.Result {
	var args proto.ScopesArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return proto.Result{Error: "invalid args: " + err.Error()}
	}

	scopes, err := d.session.Scopes(args.FrameID)
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	infos := make([]proto.ScopeInfo, len(scopes))
	for i, s := range scopes {
		infos[i] = proto.ScopeInfo{Name: s.Name}
	}
	return proto.Result{OK: true, Data: infos}
}

func (d *Daemon) cmdVariables(raw json.RawMessage) proto.Result {
	var args proto.VariablesArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return proto.Result{Error: "invalid args: " + err.Error()}
	}

	depth := args.Depth
	if depth == 0 {
		depth = 1
	}

	vars, err := d.session.Variables(args.Ref)
	if err != nil {
		return proto.Result{Error: err.Error()}
	}

	return proto.Result{OK: true, Data: d.expandVariables(vars, depth)}
}

func (d *Daemon) cmdOutput(raw json.RawMessage) proto.Result {
	var args proto.OutputArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	}

	lines := args.Lines
	if lines == 0 {
		lines = 50
	}

	d.outputMu.Lock()
	defer d.outputMu.Unlock()

	start := 0
	if len(d.output) > lines {
		start = len(d.output) - lines
	}
	return proto.Result{OK: true, Data: d.output[start:]}
}

func (d *Daemon) cmdStatus() proto.Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	return proto.Result{OK: true, Data: map[string]interface{}{
		"id":      d.config.SessionID,
		"state":   d.state,
		"program": d.config.Program,
		"pid":     d.config.PID,
	}}
}

func (d *Daemon) cmdDisconnect(raw json.RawMessage) proto.Result {
	var args proto.DisconnectArgs
	if raw != nil {
		json.Unmarshal(raw, &args)
	} else {
		args.TerminateDebuggee = true
	}

	d.session.Disconnect(args.TerminateDebuggee)

	// Signal shutdown
	select {
	case <-d.quit:
	default:
		close(d.quit)
	}

	return proto.Result{OK: true, Data: "disconnected"}
}

func (d *Daemon) shutdownSession() {
	d.session.Disconnect(true)
	select {
	case <-d.quit:
	default:
		close(d.quit)
	}
}

func (d *Daemon) cleanup() {
	if d.listener != nil {
		d.listener.Close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		d.cmd.Process.Kill()
	}
	// Remove session file
	os.Remove(paths.SessionFile(d.config.SessionID))
	log.Printf("daemon: cleaned up session %q", d.config.SessionID)
}

func (d *Daemon) touchActivity() {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()

	// Update session file lastActivity
	sfPath := paths.SessionFile(d.config.SessionID)
	data, err := os.ReadFile(sfPath)
	if err == nil {
		var sf proto.SessionFile
		if json.Unmarshal(data, &sf) == nil {
			sf.LastActivity = d.lastActivity.Format(time.RFC3339)
			if updated, err := json.Marshal(sf); err == nil {
				os.WriteFile(sfPath, updated, 0600)
			}
		}
	}
}

// --- Helpers ---

func writeSessionFile(sf proto.SessionFile) error {
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(paths.SessionFile(sf.ID), data, 0600)
}

func writeResult(conn net.Conn, result proto.Result) {
	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("daemon: marshal error: %v", err)
		return
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		// Connection may have been closed by client, that's fine
		if !isConnectionClosed(err) {
			log.Printf("daemon: write error: %v", err)
		}
	}
}

func isConnectionClosed(err error) bool {
	if err == io.EOF {
		return true
	}
	if opErr, ok := err.(*net.OpError); ok {
		return opErr.Err.Error() == "write: broken pipe" ||
			opErr.Err.Error() == "write: connection reset by peer"
	}
	return false
}
