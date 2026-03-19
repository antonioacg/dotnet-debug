# dotnet-debug

CLI debugger for .NET applications, designed for autonomous use by AI agents. Wraps [netcoredbg](https://github.com/Samsung/netcoredbg) via the Debug Adapter Protocol (DAP), exposing all operations as simple JSON-returning shell commands.

**The idea:** tell Claude "debug the `/orders` endpoint, it's returning 500" and it does everything — launches the debugger, sets breakpoints, triggers the request, inspects variables, and finds the bug.

## Quick start

```bash
# 1. Install dotnet-debug (pick one)
go install .                              # from source
# or download from GitHub releases

# 2. Install the debug adapter
dotnet-debug install-netcoredbg

# 3. Debug something
dotnet-debug launch --dll bin/Debug/net8.0/MyApp.dll
dotnet-debug bp --file /path/to/Controller.cs --lines 47
dotnet-debug continue
dotnet-debug inspect
dotnet-debug stop
```

## Install

### From GitHub releases (recommended)

Download the binary for your platform from the [releases page](../../releases/latest), extract it, and put it in your PATH.

Or with `go install`:
```bash
go install github.com/YOUR_ORG/dotnet-debug@latest
```

### From source

```bash
git clone <this-repo>
cd csharp-debug
go build -o dotnet-debug .
cp dotnet-debug ~/.local/bin/   # or /usr/local/bin/
```

### Releases

Releases are built automatically by [GoReleaser](.goreleaser.yml) when a version tag is pushed:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Cross-compiles for macOS (arm64/amd64), Linux (arm64/amd64), and Windows (amd64).

### Install netcoredbg

netcoredbg is the debug adapter that talks to the .NET runtime. The official Samsung builds don't include macOS arm64 — this command handles it:

```bash
dotnet-debug install-netcoredbg
```

Downloads the correct binary for your platform to `~/.dotnet-debug/bin/netcoredbg/`. The tool finds it automatically. Requires `gh` (GitHub CLI).

Alternatively, set `NETCOREDBG_PATH` to point to any working netcoredbg binary.

### Install Claude Code skill (optional)

The included skill teaches Claude how to use dotnet-debug for autonomous debugging:

```bash
# Install for all your projects (user-level)
dotnet-debug install-skill --user

# Install for a specific project only
dotnet-debug install-skill --project /path/to/my-dotnet-app

# Then in Claude Code:
# /debug-dotnet "the /orders endpoint returns 500"
```

## Architecture

```
┌─────────────┐     TCP/JSON      ┌──────────────┐    DAP/stdio    ┌─────────────┐
│  CLI command │ ───────────────── │    Daemon     │ ─────────────── │  netcoredbg  │
│  (short-lived)│                  │  (background) │                 │  (subprocess)│
└─────────────┘                    └──────────────┘                  └─────────────┘
                                          │
                                   ~/.dotnet-debug/
                                   sessions/<id>.json
```

- **Single Go binary** — daemon mode (`__daemon__`) and CLI mode in one executable
- **Background daemon** per debug session, manages netcoredbg over stdin/stdout DAP
- **CLI commands** connect to the daemon via TCP localhost, send a JSON-line command, get a JSON-line response, disconnect
- **Session files** at `~/.dotnet-debug/sessions/<id>.json` store the TCP port and auth token
- **Multiple concurrent sessions** supported — use `--session <id>` to target
- **2-hour inactivity timeout** — daemon auto-terminates and cleans up
- **Cross-platform** — macOS, Linux, Windows (TCP localhost, no Unix sockets)

## Commands

### Session management

| Command | Description |
|---------|-------------|
| `launch --dll <path>` | Start a debug session. Program paused until `continue`. |
| `attach --pid <pid>` | Attach to a running .NET process. |
| `sessions` | List all active debug sessions. |
| `stop [--all]` | Stop a session (or all sessions). |
| `status` | Show session state. |

### Breakpoints

| Command | Description |
|---------|-------------|
| `bp --file <path> --lines <n,n,...>` | Set line breakpoints. Use absolute paths. |
| `exception-bp [--filters all\|user-unhandled]` | Break on exceptions. |

### Execution control

| Command | Description |
|---------|-------------|
| `continue` / `c` | Resume. Waits for next stop by default. |
| `next` / `n` | Step over. |
| `step-in` / `si` | Step into. |
| `step-out` / `so` | Step out. |
| `pause` | Pause execution. |
| `wait [--timeout <dur>]` | Wait for a stop event (breakpoint, exception). |

### Inspection

| Command | Description |
|---------|-------------|
| `inspect` / `i` | Full state snapshot: stack, locals, threads, exception. |
| `eval` / `e` `<expr>` | Evaluate an expression in the current frame. |
| `threads` | List all threads. |
| `stack [--levels <n>]` | Stack trace for the stopped thread. |
| `output [--lines <n>]` | Recent debuggee stdout. |

### Common flags

- `--session <id>` — target a specific session (optional if only one active)
- `--timeout <duration>` — timeout for blocking operations (default 30s)
- `--thread <id>` — target a specific thread (default: stopped thread)
- `--depth <n>` — variable expansion depth for `inspect` (default 2)

All output is JSON. Errors return `{"ok": false, "error": "..."}`.

## Example session

```bash
# Launch
$ dotnet-debug launch --dll bin/Debug/net8.0/MyApi.dll
{"ok":true,"data":{"session":"myapi-1","port":48901,"program":"..."}}

# Set breakpoint and exception catching
$ dotnet-debug bp --file /src/Controllers/OrderController.cs --lines 47
{"ok":true,"data":[{"id":1,"verified":false,"line":47,"file":"..."}]}

$ dotnet-debug exception-bp --filters all

# Resume and trigger
$ dotnet-debug continue --no-wait
$ curl http://localhost:5000/api/orders/42
$ dotnet-debug wait --timeout 60s
{"ok":true,"data":{"reason":"breakpoint","file":"...OrderController.cs","line":47,"threadId":1}}

# Inspect everything
$ dotnet-debug inspect
{"ok":true,"data":{"stopped":{...},"stackTrace":[...],"scopes":[{"name":"Locals","variables":[{"name":"orderId","value":"42","type":"int"},{"name":"order","value":"null","type":"Order"}]}]}}

# Evaluate
$ dotnet-debug eval "orderId"
{"ok":true,"data":{"result":"42","type":"int"}}

# Step and inspect
$ dotnet-debug next
{"ok":true,"data":{"reason":"step","file":"...","line":48}}

$ dotnet-debug inspect --depth 3

# Done
$ dotnet-debug stop
```

## Troubleshooting

**Breakpoints stay "pending":** Source file paths must match PDB paths exactly. On macOS, use `/private/tmp/...` not `/tmp/...`. Rebuild with `dotnet build -c Debug`.

**configurationDone fails:** You're using a broken netcoredbg build. Run `./scripts/install-netcoredbg.sh` to get the working Cliffback pre-built binary.

**Daemon won't start:** Check `~/.dotnet-debug/logs/<session-id>.log` for details.

**Multiple sessions conflict:** Use `--session <id>` on every command, or `dotnet-debug stop --all` to clean up.

## Platform support

| Platform | Status |
|----------|--------|
| macOS arm64 | Tested, uses Cliffback netcoredbg build |
| macOS x86_64 | Should work with Samsung official build |
| Linux x86_64 | Should work with Samsung official build |
| Linux arm64 | Should work with Samsung official build |
| Windows | Should work with Samsung official build (untested) |

## License

MIT
