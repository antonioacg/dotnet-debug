package proto

import "encoding/json"

// --- Daemon <-> CLI protocol (JSON-line over TCP) ---

// Command is sent from the CLI to the daemon.
type Command struct {
	Cmd   string          `json:"cmd"`
	Token string          `json:"token"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// Result is sent from the daemon back to the CLI.
type Result struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

// --- Command argument types ---

type BreakpointArgs struct {
	File       string   `json:"file"`
	Lines      []int    `json:"lines"`
	Conditions []string `json:"conditions,omitempty"`
}

type ExceptionBreakpointArgs struct {
	Filters []string `json:"filters"` // "all", "user-unhandled"
}

type ContinueArgs struct {
	ThreadID    int `json:"threadId,omitempty"`
	WaitForStop int `json:"waitForStop,omitempty"` // 1=yes (default), 0=no
	TimeoutMs   int `json:"timeoutMs,omitempty"`   // default 30000
}

type StepArgs struct {
	ThreadID    int `json:"threadId,omitempty"`
	WaitForStop int `json:"waitForStop,omitempty"`
	TimeoutMs   int `json:"timeoutMs,omitempty"`
}

type PauseArgs struct {
	ThreadID int `json:"threadId,omitempty"`
}

type EvalArgs struct {
	Expression string `json:"expression"`
	FrameID    int    `json:"frameId,omitempty"`
}

type InspectArgs struct {
	ThreadID int `json:"threadId,omitempty"`
	Depth    int `json:"depth,omitempty"` // variable expansion depth, default 2
}

type StackTraceArgs struct {
	ThreadID int `json:"threadId,omitempty"`
	Levels   int `json:"levels,omitempty"`
}

type ScopesArgs struct {
	FrameID int `json:"frameId"`
}

type VariablesArgs struct {
	Ref   int `json:"ref"`
	Depth int `json:"depth,omitempty"`
}

type WaitArgs struct {
	TimeoutMs int `json:"timeoutMs,omitempty"` // default 30000
}

type OutputArgs struct {
	Lines int `json:"lines,omitempty"` // recent lines, default 50
}

type DisconnectArgs struct {
	TerminateDebuggee bool `json:"terminateDebuggee"`
}

// --- Result data types ---

type StopInfo struct {
	Reason      string `json:"reason"` // "breakpoint", "exception", "step", "pause", "entry"
	Description string `json:"description,omitempty"`
	ThreadID    int    `json:"threadId"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Column      int    `json:"column,omitempty"`
}

type InspectResult struct {
	Stopped    *StopInfo      `json:"stopped,omitempty"`
	Threads    []ThreadInfo   `json:"threads,omitempty"`
	StackTrace []FrameInfo    `json:"stackTrace,omitempty"`
	Scopes     []ScopeInfo    `json:"scopes,omitempty"`
	Exception  *ExceptionInfo `json:"exception,omitempty"`
}

type ThreadInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type FrameInfo struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

type ScopeInfo struct {
	Name      string         `json:"name"`
	Variables []VariableInfo `json:"variables,omitempty"`
}

type VariableInfo struct {
	Name     string         `json:"name"`
	Value    string         `json:"value"`
	Type     string         `json:"type,omitempty"`
	Children []VariableInfo `json:"children,omitempty"`
}

type ExceptionInfo struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	StackTrace  string `json:"stackTrace,omitempty"`
}

type BreakpointInfo struct {
	ID       int    `json:"id"`
	Verified bool   `json:"verified"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line"`
	Message  string `json:"message,omitempty"`
}

// --- Session file format (persisted to disk) ---

type SessionFile struct {
	ID           string `json:"id"`
	Port         int    `json:"port"`
	DaemonPID    int    `json:"daemonPid"`
	Token        string `json:"token"`
	Program      string `json:"program,omitempty"`
	AttachedPID  int    `json:"attachedPid,omitempty"`
	Created      string `json:"created"`
	LastActivity string `json:"lastActivity"`
}

// --- Daemon startup config (passed as CLI arg to __daemon__) ---

type DaemonConfig struct {
	Mode           string            `json:"mode"` // "launch", "attach", or "project"
	SessionID      string            `json:"sessionId"`
	NetcoredbgPath string            `json:"netcoredbgPath"`
	Program        string            `json:"program,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	StopAtEntry    bool              `json:"stopAtEntry,omitempty"`
	PID            int               `json:"pid,omitempty"`           // for attach mode
	Project        string            `json:"project,omitempty"`       // for project mode: .csproj path
	LaunchProfile  string            `json:"launchProfile,omitempty"` // for project mode: launch profile name
}
