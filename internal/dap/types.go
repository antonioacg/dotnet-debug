package dap

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// FlexInt handles JSON numbers that may arrive as either int or string.
// netcoredbg sends seq/request_seq as strings, violating the DAP spec.
type FlexInt int

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*f = FlexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("FlexInt: cannot unmarshal %s", string(b))
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("FlexInt: invalid number %q", s)
	}
	*f = FlexInt(v)
	return nil
}

func (f FlexInt) MarshalJSON() ([]byte, error) {
	return json.Marshal(int(f))
}

// ProtocolMessage is the base type for all DAP messages.
type ProtocolMessage struct {
	Seq  FlexInt `json:"seq"`
	Type string  `json:"type"` // "request", "response", "event"
}

// Request is a DAP request message.
type Request struct {
	ProtocolMessage
	Command   string      `json:"command"`
	Arguments interface{} `json:"arguments,omitempty"`
}

// Response is a DAP response message.
type Response struct {
	ProtocolMessage
	RequestSeq FlexInt          `json:"request_seq"`
	Success    bool             `json:"success"`
	Command    string           `json:"command"`
	Message    string           `json:"message,omitempty"`
	Body       *json.RawMessage `json:"body,omitempty"`
}

// Event is a DAP event message.
type Event struct {
	ProtocolMessage
	Event string           `json:"event"`
	Body  *json.RawMessage `json:"body,omitempty"`
}

// --- Initialize ---

type InitializeArguments struct {
	ClientID                     string `json:"clientID"`
	ClientName                   string `json:"clientName"`
	AdapterID                    string `json:"adapterID"`
	Locale                       string `json:"locale,omitempty"`
	LinesStartAt1                bool   `json:"linesStartAt1"`
	ColumnsStartAt1              bool   `json:"columnsStartAt1"`
	PathFormat                   string `json:"pathFormat,omitempty"`
	SupportsVariableType         bool   `json:"supportsVariableType"`
	SupportsVariablePaging       bool   `json:"supportsVariablePaging"`
	SupportsRunInTerminalRequest bool   `json:"supportsRunInTerminalRequest"`
}

type Capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	SupportsSetVariable              bool `json:"supportsSetVariable"`
	SupportsConditionalBreakpoints   bool `json:"supportsConditionalBreakpoints"`
	SupportsEvaluateForHovers        bool `json:"supportsEvaluateForHovers"`
	SupportsExceptionInfoRequest     bool `json:"supportsExceptionInfoRequest"`
	ExceptionBreakpointFilters       []ExceptionBreakpointsFilter `json:"exceptionBreakpointFilters,omitempty"`
}

type ExceptionBreakpointsFilter struct {
	Filter  string `json:"filter"`
	Label   string `json:"label"`
	Default bool   `json:"default"`
}

// --- Launch / Attach ---

type LaunchArguments struct {
	Program     string            `json:"program"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	StopAtEntry bool              `json:"stopAtEntry,omitempty"`
	JustMyCode  bool              `json:"justMyCode,omitempty"`
}

type AttachArguments struct {
	ProcessID int `json:"processId"`
}

// --- Breakpoints ---

type SetBreakpointsArguments struct {
	Source      Source             `json:"source"`
	Breakpoints []SourceBreakpoint `json:"breakpoints"`
}

type Source struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

type SourceBreakpoint struct {
	Line      int    `json:"line"`
	Condition string `json:"condition,omitempty"`
	LogMessage string `json:"logMessage,omitempty"`
}

type SetBreakpointsResponseBody struct {
	Breakpoints []Breakpoint `json:"breakpoints"`
}

type Breakpoint struct {
	ID       int    `json:"id"`
	Verified bool   `json:"verified"`
	Line     int    `json:"line"`
	Message  string `json:"message,omitempty"`
	Source   *Source `json:"source,omitempty"`
}

type SetExceptionBreakpointsArguments struct {
	Filters []string `json:"filters"`
}

// --- Execution Control ---

type ContinueArguments struct {
	ThreadID int `json:"threadId"`
}

type ContinueResponseBody struct {
	AllThreadsContinued bool `json:"allThreadsContinued"`
}

type StepArguments struct {
	ThreadID int `json:"threadId"`
}

type PauseArguments struct {
	ThreadID int `json:"threadId"`
}

// --- Threads ---

type ThreadsResponseBody struct {
	Threads []Thread `json:"threads"`
}

type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// --- Stack Trace ---

type StackTraceArguments struct {
	ThreadID   int `json:"threadId"`
	StartFrame int `json:"startFrame,omitempty"`
	Levels     int `json:"levels,omitempty"`
}

type StackTraceResponseBody struct {
	StackFrames []StackFrame `json:"stackFrames"`
	TotalFrames int          `json:"totalFrames,omitempty"`
}

type StackFrame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Source *Source  `json:"source,omitempty"`
	Line   int     `json:"line"`
	Column int     `json:"column"`
}

// --- Scopes ---

type ScopesArguments struct {
	FrameID int `json:"frameId"`
}

type ScopesResponseBody struct {
	Scopes []Scope `json:"scopes"`
}

type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
}

// --- Variables ---

type VariablesArguments struct {
	VariablesReference int `json:"variablesReference"`
}

type VariablesResponseBody struct {
	Variables []Variable `json:"variables"`
}

type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
}

// --- Evaluate ---

type EvaluateArguments struct {
	Expression string `json:"expression"`
	FrameID    int    `json:"frameId,omitempty"`
	Context    string `json:"context,omitempty"` // "watch", "repl", "hover"
}

type EvaluateResponseBody struct {
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// --- Exception Info ---

type ExceptionInfoArguments struct {
	ThreadID int `json:"threadId"`
}

type ExceptionInfoResponseBody struct {
	ExceptionID string           `json:"exceptionId"`
	Description string           `json:"description,omitempty"`
	BreakMode   string           `json:"breakMode"`
	Details     *ExceptionDetail `json:"details,omitempty"`
}

type ExceptionDetail struct {
	Message        string           `json:"message,omitempty"`
	TypeName       string           `json:"typeName,omitempty"`
	StackTrace     string           `json:"stackTrace,omitempty"`
	InnerException []ExceptionDetail `json:"innerException,omitempty"`
}

// --- Disconnect ---

type DisconnectArguments struct {
	Restart           bool `json:"restart,omitempty"`
	TerminateDebuggee bool `json:"terminateDebuggee,omitempty"`
}

// --- Events ---

type StoppedEventBody struct {
	Reason            string `json:"reason"` // "step", "breakpoint", "exception", "pause", "entry"
	Description       string `json:"description,omitempty"`
	ThreadID          int    `json:"threadId"`
	AllThreadsStopped bool   `json:"allThreadsStopped"`
	Text              string `json:"text,omitempty"`
}

type ExitedEventBody struct {
	ExitCode int `json:"exitCode"`
}

type TerminatedEventBody struct {
	Restart bool `json:"restart,omitempty"`
}

type OutputEventBody struct {
	Category string  `json:"category,omitempty"` // "console", "stdout", "stderr", "telemetry"
	Output   string  `json:"output"`
	Source   *Source  `json:"source,omitempty"`
	Line     int     `json:"line,omitempty"`
}
