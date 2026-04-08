package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dotnet-debug/internal/daemon"
	"dotnet-debug/internal/paths"
	"dotnet-debug/internal/proto"
)

// version is set at build time via ldflags.
var version = "dev"

// envMapFlag implements flag.Value for repeatable --env KEY=VALUE flags.
type envMapFlag map[string]string

func (e *envMapFlag) String() string { return "" }
func (e *envMapFlag) Set(val string) error {
	parts := strings.SplitN(val, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return fmt.Errorf("invalid env format %q, expected KEY=VALUE", val)
	}
	(*e)[parts[0]] = parts[1]
	return nil
}

//go:embed skills/dotnet-debug/SKILL.md
var skillContent []byte

func main() {
	log.SetFlags(log.Ltime)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "__daemon__":
		if len(args) < 1 {
			fatal("daemon: missing config argument")
		}
		if err := daemon.Run(args[0]); err != nil {
			log.Fatalf("daemon: %v", err)
		}

	case "launch":
		cmdLaunch(args)
	case "attach":
		cmdAttach(args)
	case "bp":
		cmdBreakpoint(args)
	case "exception-bp":
		cmdExceptionBreakpoint(args)
	case "continue", "c":
		cmdContinue(args)
	case "next", "n":
		cmdStep("next", args)
	case "step-in", "si":
		cmdStep("step-in", args)
	case "step-out", "so":
		cmdStep("step-out", args)
	case "pause":
		cmdPause(args)
	case "wait":
		cmdWait(args)
	case "inspect", "i":
		cmdInspect(args)
	case "eval", "e":
		cmdEval(args)
	case "threads":
		cmdSimple("threads", nil, args)
	case "stack":
		cmdStackTrace(args)
	case "output":
		cmdOutput(args)
	case "status":
		cmdSimple("status", nil, args)
	case "stop":
		cmdStop(args)
	case "sessions":
		cmdSessions()
	case "install-skill":
		cmdInstallSkill(args)
	case "install-netcoredbg":
		cmdInstallNetcoredbg(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

// --- Launch / Attach ---

func cmdLaunch(args []string) {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	dll := fs.String("dll", "", "path to DLL")
	project := fs.String("project", "", "path to .csproj (alternative to --dll)")
	launchProfile := fs.String("launch-profile", "", "launch profile name (with --project)")
	cwd := fs.String("cwd", "", "working directory")
	progArgs := fs.String("args", "", "program arguments (space-separated)")
	session := fs.String("session", "", "session ID (auto-generated if omitted)")
	stopAtEntry := fs.Bool("stop-at-entry", false, "stop at entry point")
	netcoredbg := fs.String("netcoredbg", "", "path to netcoredbg binary")
	envVars := make(envMapFlag)
	fs.Var(&envVars, "env", "environment variable KEY=VALUE (repeatable)")
	force := fs.Bool("force", false, "skip port conflict check")
	fs.Parse(args)

	if *dll == "" && *project == "" {
		fatal("either --dll or --project is required")
	}
	if *dll != "" && *project != "" {
		fatal("--dll and --project are mutually exclusive")
	}
	if *project != "" && *stopAtEntry {
		fatal("--stop-at-entry is not compatible with --project mode (debugger attaches after app starts)")
	}

	if err := paths.EnsureDirs(); err != nil {
		fatal("creating directories: %v", err)
	}

	// Resolve target path (DLL or csproj)
	var targetPath string
	if *dll != "" {
		absPath, err := filepath.Abs(*dll)
		if err != nil {
			fatal("resolving DLL path: %v", err)
		}
		if _, err := os.Stat(absPath); err != nil {
			fatal("DLL not found: %s", absPath)
		}
		targetPath = absPath
	} else {
		absPath, err := filepath.Abs(*project)
		if err != nil {
			fatal("resolving project path: %v", err)
		}
		if _, err := os.Stat(absPath); err != nil {
			fatal("project file not found: %s", absPath)
		}
		targetPath = absPath
	}

	if *session == "" {
		*session = paths.GenerateSessionID(targetPath)
	}
	if sf, err := loadSession(*session); err == nil {
		if isProcessAlive(sf.DaemonPID) {
			fatal("session %q already exists and is running (PID %d). Use 'stop --session %s' first.", *session, sf.DaemonPID, *session)
		}
		os.Remove(paths.SessionFile(*session))
		log.Printf("cleaned up stale session %q (PID %d no longer running)", *session, sf.DaemonPID)
	}

	dbgPath := *netcoredbg
	if dbgPath == "" {
		dbgPath = paths.FindNetcoredbg()
	}
	if dbgPath == "" {
		fatal("netcoredbg not found. Set NETCOREDBG_PATH or use --netcoredbg flag.")
	}

	var pArgs []string
	if *progArgs != "" {
		pArgs = strings.Fields(*progArgs)
	}

	// Check for port conflicts before launching
	if !*force {
		urls := envVars["ASPNETCORE_URLS"]
		if urls == "" {
			urls = os.Getenv("ASPNETCORE_URLS")
		}
		if urls != "" {
			for _, port := range parsePortsFromURLs(urls) {
				if available, pid := checkPortAvailable(port); !available {
					msg := fmt.Sprintf("port %d already in use", port)
					if pid > 0 {
						msg += fmt.Sprintf(" by PID %d", pid)
					}
					msg += ". Kill the existing process or use --force to skip this check."
					fatal(msg)
				}
			}
		}
	}

	var config proto.DaemonConfig
	if *dll != "" {
		config = proto.DaemonConfig{
			Mode:           "launch",
			SessionID:      *session,
			NetcoredbgPath: dbgPath,
			Program:        targetPath,
			Args:           pArgs,
			Cwd:            *cwd,
			Env:            map[string]string(envVars),
			StopAtEntry:    *stopAtEntry,
		}
	} else {
		config = proto.DaemonConfig{
			Mode:           "project",
			SessionID:      *session,
			NetcoredbgPath: dbgPath,
			Project:        targetPath,
			LaunchProfile:  *launchProfile,
			Args:           pArgs,
			Cwd:            *cwd,
			Env:            map[string]string(envVars),
		}
	}

	startDaemonProcess(config)

	sf, err := waitForSession(*session, 45*time.Second) // project mode needs more time
	if err != nil {
		logPath := paths.LogFile(*session)
		if logData, e := os.ReadFile(logPath); e == nil && len(logData) > 0 {
			fmt.Fprintf(os.Stderr, "--- daemon log ---\n%s\n", string(logData))
		}
		fatal("daemon failed to start: %v", err)
	}

	printResult(proto.Result{OK: true, Data: map[string]interface{}{
		"session": sf.ID,
		"port":    sf.Port,
		"program": sf.Program,
	}})
}

func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	pid := fs.Int("pid", 0, "process ID (required)")
	session := fs.String("session", "", "session ID")
	netcoredbg := fs.String("netcoredbg", "", "path to netcoredbg binary")
	fs.Parse(args)

	if *pid == 0 {
		fatal("--pid is required")
	}

	if err := paths.EnsureDirs(); err != nil {
		fatal("creating directories: %v", err)
	}

	if *session == "" {
		*session = fmt.Sprintf("pid-%d", *pid)
	}
	if sf, err := loadSession(*session); err == nil {
		if isProcessAlive(sf.DaemonPID) {
			fatal("session %q already exists and is running (PID %d). Use 'stop --session %s' first.", *session, sf.DaemonPID, *session)
		}
		os.Remove(paths.SessionFile(*session))
		log.Printf("cleaned up stale session %q (PID %d no longer running)", *session, sf.DaemonPID)
	}

	dbgPath := *netcoredbg
	if dbgPath == "" {
		dbgPath = paths.FindNetcoredbg()
	}
	if dbgPath == "" {
		fatal("netcoredbg not found.")
	}

	config := proto.DaemonConfig{
		Mode:           "attach",
		SessionID:      *session,
		NetcoredbgPath: dbgPath,
		PID:            *pid,
	}

	startDaemonProcess(config)

	sf, err := waitForSession(*session, 30*time.Second)
	if err != nil {
		fatal("daemon failed to start: %v", err)
	}

	printResult(proto.Result{OK: true, Data: map[string]interface{}{
		"session": sf.ID,
		"port":    sf.Port,
		"pid":     sf.AttachedPID,
	}})
}

// --- Breakpoints ---

func cmdBreakpoint(args []string) {
	fs := flag.NewFlagSet("bp", flag.ExitOnError)
	file := fs.String("file", "", "source file path (required)")
	lines := fs.String("lines", "", "line numbers, comma-separated (required)")
	condition := fs.String("condition", "", "breakpoint condition")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	if *file == "" || *lines == "" {
		fatal("--file and --lines are required")
	}

	absFile, err := filepath.Abs(*file)
	if err != nil {
		fatal("resolving file path: %v", err)
	}

	lineNums := parseIntList(*lines)
	if len(lineNums) == 0 {
		fatal("--lines must contain at least one line number")
	}

	bpArgs := proto.BreakpointArgs{File: absFile, Lines: lineNums}
	if *condition != "" {
		conditions := make([]string, len(lineNums))
		for i := range conditions {
			conditions[i] = *condition
		}
		bpArgs.Conditions = conditions
	}

	result := sendCommand(*session, "breakpoints", bpArgs)
	printResult(result)
}

func cmdExceptionBreakpoint(args []string) {
	fs := flag.NewFlagSet("exception-bp", flag.ExitOnError)
	filters := fs.String("filters", "all", "exception filters: all, user-unhandled")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	filterList := strings.Split(*filters, ",")
	for i := range filterList {
		filterList[i] = strings.TrimSpace(filterList[i])
	}

	result := sendCommand(*session, "exception-breakpoints", proto.ExceptionBreakpointArgs{Filters: filterList})
	printResult(result)
}

// --- Execution control ---

func cmdContinue(args []string) {
	fs := flag.NewFlagSet("continue", flag.ExitOnError)
	thread := fs.Int("thread", 0, "thread ID")
	noWait := fs.Bool("no-wait", false, "don't wait for stop")
	timeout := fs.Duration("timeout", 30*time.Second, "wait timeout")
	session := fs.String("session", "", "session ID")
	healthURL := fs.String("health-url", "", "URL to poll for 200 OK after continue")
	healthTimeout := fs.Duration("health-timeout", 30*time.Second, "health check timeout")
	fs.Parse(args)

	waitFlag := 1
	if *noWait || *healthURL != "" {
		// Health polling requires --no-wait semantics (can't wait for breakpoint and poll simultaneously)
		waitFlag = 0
	}

	result := sendCommand(*session, "continue", proto.ContinueArgs{
		ThreadID:    *thread,
		WaitForStop: waitFlag,
		TimeoutMs:   int(timeout.Milliseconds()),
	})
	printResult(result)

	if *healthURL != "" && result.OK {
		if err := pollHealth(*healthURL, *healthTimeout); err != nil {
			printResult(proto.Result{Error: fmt.Sprintf("health check failed: %v", err)})
		} else {
			printResult(proto.Result{OK: true, Data: map[string]interface{}{
				"health": "ok",
				"url":    *healthURL,
			}})
		}
	}
}

func cmdStep(kind string, args []string) {
	fs := flag.NewFlagSet(kind, flag.ExitOnError)
	thread := fs.Int("thread", 0, "thread ID")
	timeout := fs.Duration("timeout", 30*time.Second, "wait timeout")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, kind, proto.StepArgs{
		ThreadID:    *thread,
		WaitForStop: 1,
		TimeoutMs:   int(timeout.Milliseconds()),
	})
	printResult(result)
}

func cmdPause(args []string) {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	thread := fs.Int("thread", 0, "thread ID")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, "pause", proto.PauseArgs{ThreadID: *thread})
	printResult(result)
}

func cmdWait(args []string) {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	timeout := fs.Duration("timeout", 30*time.Second, "wait timeout")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, "wait", proto.WaitArgs{TimeoutMs: int(timeout.Milliseconds())})
	printResult(result)
}

// --- Inspection ---

func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	thread := fs.Int("thread", 0, "thread ID")
	depth := fs.Int("depth", 2, "variable expansion depth")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, "inspect", proto.InspectArgs{
		ThreadID: *thread,
		Depth:    *depth,
	})
	printResult(result)
}

func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	frame := fs.Int("frame", 0, "frame ID")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fatal("expression argument required")
	}
	expr := strings.Join(remaining, " ")

	result := sendCommand(*session, "eval", proto.EvalArgs{
		Expression: expr,
		FrameID:    *frame,
	})
	printResult(result)
}

func cmdStackTrace(args []string) {
	fs := flag.NewFlagSet("stack", flag.ExitOnError)
	thread := fs.Int("thread", 0, "thread ID")
	levels := fs.Int("levels", 20, "max frames")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, "stack", proto.StackTraceArgs{
		ThreadID: *thread,
		Levels:   *levels,
	})
	printResult(result)
}

func cmdOutput(args []string) {
	fs := flag.NewFlagSet("output", flag.ExitOnError)
	lines := fs.Int("lines", 50, "number of recent lines")
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, "output", proto.OutputArgs{Lines: *lines})
	printResult(result)
}

func cmdSimple(cmd string, cmdArgs interface{}, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	session := fs.String("session", "", "session ID")
	fs.Parse(args)

	result := sendCommand(*session, cmd, cmdArgs)
	printResult(result)
}

// --- Session management ---

func cmdStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	session := fs.String("session", "", "session ID")
	all := fs.Bool("all", false, "stop all sessions")
	fs.Parse(args)

	if *all {
		sessions := listAllSessions()
		for _, sf := range sessions {
			sendCommandToSession(sf, "disconnect", proto.DisconnectArgs{TerminateDebuggee: true})
			fmt.Fprintf(os.Stderr, "stopped session: %s\n", sf.ID)
		}
		printResult(proto.Result{OK: true, Data: fmt.Sprintf("stopped %d sessions", len(sessions))})
		return
	}

	result := sendCommand(*session, "disconnect", proto.DisconnectArgs{TerminateDebuggee: true})
	printResult(result)
}

func cmdSessions() {
	sessions := listAllSessions()
	if len(sessions) == 0 {
		printResult(proto.Result{OK: true, Data: []interface{}{}})
		return
	}

	// Check which sessions are alive
	var alive []map[string]interface{}
	for _, sf := range sessions {
		entry := map[string]interface{}{
			"id":           sf.ID,
			"program":      sf.Program,
			"port":         sf.Port,
			"created":      sf.Created,
			"lastActivity": sf.LastActivity,
		}
		if isAlive(sf) {
			entry["alive"] = true
		} else {
			entry["alive"] = false
			// Prune stale session file
			os.Remove(paths.SessionFile(sf.ID))
		}
		alive = append(alive, entry)
	}

	printResult(proto.Result{OK: true, Data: alive})
}

// --- Skill installation ---

func cmdInstallSkill(args []string) {
	fs := flag.NewFlagSet("install-skill", flag.ExitOnError)
	user := fs.Bool("user", false, "install to ~/.claude/skills/ (all projects)")
	project := fs.String("project", "", "install to <path>/.claude/skills/ (specific project)")
	fs.Parse(args)

	var targetDir string
	switch {
	case *user:
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("finding home directory: %v", err)
		}
		targetDir = filepath.Join(home, ".claude", "skills", "dotnet-debug")
	case *project != "":
		absProject, err := filepath.Abs(*project)
		if err != nil {
			fatal("resolving project path: %v", err)
		}
		targetDir = filepath.Join(absProject, ".claude", "skills", "dotnet-debug")
	default:
		// Default: current directory (project-level)
		cwd, err := os.Getwd()
		if err != nil {
			fatal("getting working directory: %v", err)
		}
		targetDir = filepath.Join(cwd, ".claude", "skills", "dotnet-debug")
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		fatal("creating skill directory: %v", err)
	}

	skillPath := filepath.Join(targetDir, "SKILL.md")
	if err := os.WriteFile(skillPath, skillContent, 0644); err != nil {
		fatal("writing skill file: %v", err)
	}

	printResult(proto.Result{OK: true, Data: map[string]interface{}{
		"installed": skillPath,
		"invoke":    "/dotnet-debug <problem description>",
	}})
}

// --- netcoredbg installation ---

func cmdInstallNetcoredbg(args []string) {
	fs := flag.NewFlagSet("install-netcoredbg", flag.ExitOnError)
	ver := fs.String("version", "latest", "version to install (e.g. 3.1.3-1062)")
	fs.Parse(args)

	installDir := filepath.Join(paths.BaseDir(), "bin", "netcoredbg")

	// Check if already installed
	binary := filepath.Join(installDir, "netcoredbg")
	if _, err := os.Stat(binary); err == nil {
		out, _ := exec.Command(binary, "--version").CombinedOutput()
		ver := strings.TrimSpace(string(out))
		if ver != "" {
			printResult(proto.Result{OK: true, Data: map[string]interface{}{
				"status":  "already installed",
				"path":    binary,
				"version": strings.Split(ver, "\n")[0],
			}})
			return
		}
	}

	if err := paths.EnsureDirs(); err != nil {
		fatal("creating directories: %v", err)
	}

	// Detect platform
	goos, goarch := detectPlatform()

	var repo, asset string
	switch {
	case goos == "darwin" && goarch == "arm64":
		repo = "Cliffback/netcoredbg-macOS-arm64.nvim"
		asset = "netcoredbg-osx-arm64.tar.gz"
	case goos == "darwin" && goarch == "amd64":
		repo = "Samsung/netcoredbg"
		asset = "netcoredbg-osx-amd64.tar.gz"
	case goos == "linux" && goarch == "arm64":
		repo = "Samsung/netcoredbg"
		asset = "netcoredbg-linux-arm64.tar.gz"
	case goos == "linux" && goarch == "amd64":
		repo = "Samsung/netcoredbg"
		asset = "netcoredbg-linux-amd64.tar.gz"
	default:
		fatal("unsupported platform: %s/%s. Download manually from https://github.com/Samsung/netcoredbg/releases", goos, goarch)
	}

	// Resolve version
	version := *ver
	if version == "latest" {
		fmt.Fprintf(os.Stderr, "resolving latest version from %s...\n", repo)
		out, err := exec.Command("gh", "release", "list", "-R", repo, "--limit", "1", "--json", "tagName", "-q", ".[0].tagName").Output()
		if err != nil {
			fatal("resolving latest version (is `gh` installed?): %v", err)
		}
		version = strings.TrimSpace(string(out))
		if version == "" {
			fatal("could not determine latest version from %s", repo)
		}
	}
	fmt.Fprintf(os.Stderr, "installing netcoredbg %s from %s...\n", version, repo)

	// Download
	tmpDir, err := os.MkdirTemp("", "netcoredbg-install-*")
	if err != nil {
		fatal("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, asset)
	dlCmd := exec.Command("gh", "release", "download", version, "-R", repo, "-p", asset, "-D", tmpDir, "--clobber")
	dlCmd.Stderr = os.Stderr
	if err := dlCmd.Run(); err != nil {
		fatal("downloading: %v", err)
	}

	// Extract
	parentDir := filepath.Dir(installDir)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		fatal("creating install dir: %v", err)
	}
	// Remove old install if present
	os.RemoveAll(installDir)

	extractCmd := exec.Command("tar", "xzf", tmpFile, "-C", parentDir)
	extractCmd.Stderr = os.Stderr
	if err := extractCmd.Run(); err != nil {
		fatal("extracting: %v", err)
	}

	// Verify
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		fatal("installed binary doesn't work: %v\n%s", err, string(out))
	}

	printResult(proto.Result{OK: true, Data: map[string]interface{}{
		"installed": binary,
		"version":   strings.TrimSpace(strings.Split(string(out), "\n")[0]),
	}})
}

func detectPlatform() (string, string) {
	// Use Go's runtime for cross-platform detection
	out, err := exec.Command("go", "env", "GOOS").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		goos := strings.TrimSpace(string(out))
		out2, _ := exec.Command("go", "env", "GOARCH").Output()
		goarch := strings.TrimSpace(string(out2))
		return goos, goarch
	}
	// Fallback to uname
	out, _ = exec.Command("uname", "-s").Output()
	goos := strings.ToLower(strings.TrimSpace(string(out)))
	if goos == "darwin" || goos == "linux" {
		// ok
	} else {
		goos = "windows"
	}
	out, _ = exec.Command("uname", "-m").Output()
	arch := strings.TrimSpace(string(out))
	goarch := "amd64"
	if arch == "arm64" || arch == "aarch64" {
		goarch = "arm64"
	}
	return goos, goarch
}

// --- Daemon process management ---

func startDaemonProcess(config proto.DaemonConfig) {
	configJSON, err := json.Marshal(config)
	if err != nil {
		fatal("marshaling config: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		fatal("finding executable: %v", err)
	}

	logPath := paths.LogFile(config.SessionID)
	logFile, err := os.Create(logPath)
	if err != nil {
		fatal("creating log file: %v", err)
	}

	cmd := exec.Command(exe, "__daemon__", string(configJSON))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	setProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		fatal("starting daemon: %v", err)
	}

	// Detach - don't wait for the daemon
	go cmd.Wait()
}

func waitForSession(id string, timeout time.Duration) (*proto.SessionFile, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sf, err := loadSession(id)
		if err == nil {
			// Verify the daemon is actually listening
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sf.Port), time.Second)
			if err == nil {
				conn.Close()
				return sf, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout after %v", timeout)
}

// --- Session file helpers ---

func loadSession(id string) (*proto.SessionFile, error) {
	data, err := os.ReadFile(paths.SessionFile(id))
	if err != nil {
		return nil, err
	}
	var sf proto.SessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return &sf, nil
}

func resolveSession(sessionID string) (*proto.SessionFile, error) {
	if sessionID != "" {
		return loadSession(sessionID)
	}

	sessions := listAllSessions()
	switch len(sessions) {
	case 0:
		return nil, fmt.Errorf("no active sessions. Use 'launch' or 'attach' first")
	case 1:
		return &sessions[0], nil
	default:
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = s.ID
		}
		return nil, fmt.Errorf("multiple sessions active: [%s]. Use --session to specify", strings.Join(ids, ", "))
	}
}

func listAllSessions() []proto.SessionFile {
	files, err := paths.ListSessionFiles()
	if err != nil {
		return nil
	}
	var sessions []proto.SessionFile
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var sf proto.SessionFile
		if json.Unmarshal(data, &sf) == nil {
			sessions = append(sessions, sf)
		}
	}
	return sessions
}

func isAlive(sf proto.SessionFile) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sf.Port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// --- Command sending ---

func sendCommand(sessionID string, cmd string, args interface{}) proto.Result {
	sf, err := resolveSession(sessionID)
	if err != nil {
		return proto.Result{Error: err.Error()}
	}
	return sendCommandToSession(*sf, cmd, args)
}

func sendCommandToSession(sf proto.SessionFile, cmd string, args interface{}) proto.Result {
	var argsJSON json.RawMessage
	if args != nil {
		argsJSON, _ = json.Marshal(args)
	}

	command := proto.Command{
		Cmd:   cmd,
		Token: sf.Token,
		Args:  argsJSON,
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sf.Port), 5*time.Second)
	if err != nil {
		return proto.Result{Error: fmt.Sprintf("connecting to daemon: %v (session may have ended)", err)}
	}
	defer conn.Close()

	// Set generous deadline for long operations like wait/continue
	conn.SetDeadline(time.Now().Add(10 * time.Minute))

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(command); err != nil {
		return proto.Result{Error: fmt.Sprintf("sending command: %v", err)}
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return proto.Result{Error: "daemon closed connection (may have crashed — check log)"}
		}
		return proto.Result{Error: fmt.Sprintf("reading response: %v", err)}
	}

	var result proto.Result
	if err := json.Unmarshal(line, &result); err != nil {
		return proto.Result{Error: fmt.Sprintf("parsing response: %v", err)}
	}
	return result
}

// --- Output helpers ---

func printResult(result proto.Result) {
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(data))
	if !result.OK {
		os.Exit(1)
	}
}

func fatal(format string, args ...interface{}) {
	printResult(proto.Result{Error: fmt.Sprintf(format, args...)})
	os.Exit(1)
}

func parseIntList(s string) []int {
	parts := strings.Split(s, ",")
	var nums []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			nums = append(nums, n)
		}
	}
	return nums
}

// pollHealth polls a URL until it returns HTTP 200 or the timeout expires.
func pollHealth(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("health endpoint %s did not return 200 within %v", url, timeout)
}

// parsePortsFromURLs extracts port numbers from a semicolon-separated URL list
// like "http://localhost:5057;https://localhost:5058".
func parsePortsFromURLs(urls string) []int {
	var ports []int
	for _, u := range strings.Split(urls, ";") {
		u = strings.TrimSpace(u)
		if idx := strings.LastIndex(u, ":"); idx >= 0 {
			portStr := strings.TrimRight(u[idx+1:], "/")
			var p int
			if _, err := fmt.Sscanf(portStr, "%d", &p); err == nil && p > 0 {
				ports = append(ports, p)
			}
		}
	}
	return ports
}

// checkPortAvailable checks if a TCP port is free. Returns (true, 0) if available,
// or (false, pid) if in use (pid is best-effort, may be 0).
func checkPortAvailable(port int) (bool, int) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		ln.Close()
		return true, 0
	}
	// Best-effort: find PID using the port (macOS/Linux only)
	out, err := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-t").Output()
	if err == nil {
		pidStr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
		var pid int
		if _, err := fmt.Sscanf(pidStr, "%d", &pid); err == nil {
			return false, pid
		}
	}
	return false, 0
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `dotnet-debug — autonomous .NET debugger for AI agents

Session management:
  launch    --dll <path> [--env K=V ...] [--cwd <dir>] [--args "..."] [--session <id>]
            [--stop-at-entry] [--force]
  launch    --project <csproj> [--launch-profile <name>] [--env K=V ...] [--session <id>]
  attach    --pid <pid> [--session <id>]
  sessions  List active debug sessions
  stop      [--session <id>] [--all]    Stop a debug session
  status    [--session <id>]            Show session status

Breakpoints:
  bp           --file <path> --lines <n,n,...> [--condition <expr>]
  exception-bp [--filters all|user-unhandled]

Execution:
  continue, c   [--no-wait] [--timeout <dur>] [--health-url <url>] [--health-timeout <dur>]
  next, n       [--timeout <dur>]                 Step over
  step-in, si   [--timeout <dur>]                 Step into
  step-out, so  [--timeout <dur>]                 Step out
  pause                                           Pause execution
  wait          [--timeout <dur>]                  Wait for breakpoint hit

Inspection:
  inspect, i  [--depth <n>] [--thread <id>]    Full state snapshot
  eval, e     <expression> [--frame <id>]      Evaluate expression
  threads                                       List threads
  stack       [--levels <n>] [--thread <id>]   Stack trace
  output      [--lines <n>]                     Debuggee stdout

Setup:
  install-netcoredbg [--version <ver>]            Install netcoredbg debug adapter
  install-skill      [--user | --project <path>]  Install Claude Code skill
  version                                          Show version

All commands accept --session <id> (optional if only one session active).
All output is JSON.`)
}
