# dotnet-debug

Autonomous .NET debugger CLI for AI agents. Manages debug sessions via a background daemon that speaks DAP to netcoredbg.

## Build

```bash
go build -o dotnet-debug .
```

## Setup

netcoredbg must be installed. On macOS arm64, the Samsung build doesn't work — use the Cliffback pre-built:

```bash
./scripts/install-netcoredbg.sh
```

Or set `NETCOREDBG_PATH` to point to a working netcoredbg binary.

## Architecture

- **Single Go binary** with daemon mode (`__daemon__`) and CLI mode
- **Daemon** starts netcoredbg as subprocess, speaks DAP over stdin/stdout, accepts CLI commands over TCP (localhost)
- **CLI commands** connect to daemon via TCP, send JSON-line commands, print JSON responses
- **Session files** at `~/.dotnet-debug/sessions/<id>.json` store port + auth token
- **2-hour inactivity timeout** — daemon auto-terminates and cleans up

## Debugging workflow

```bash
# 1. Launch (starts daemon + netcoredbg, program paused until continue)
dotnet-debug launch --dll /path/to/app.dll

# 2. Set breakpoints (use absolute paths matching PDB source paths)
dotnet-debug bp --file /path/to/Controller.cs --lines 47,52

# 3. Continue (sends configurationDone + resume, waits for breakpoint hit)
dotnet-debug continue --timeout 30s

# 4. Inspect (full snapshot: stack, locals, threads)
dotnet-debug inspect

# 5. Evaluate expressions
dotnet-debug eval "order.Status"

# 6. Step through code
dotnet-debug next          # step over
dotnet-debug step-in       # step into
dotnet-debug step-out      # step out

# 7. Stop session
dotnet-debug stop
```

## Important notes

- **Source paths**: Breakpoint file paths must match PDB paths exactly. On macOS, `/tmp` resolves to `/private/tmp`. Use `realpath` if unsure.
- **Flags before args**: For `eval`, put flags before the expression: `eval --session myapi "x + 1"`
- **All output is JSON** to stdout. Errors have `"ok": false`.
- **Multiple sessions**: Use `--session <id>` to target a specific session. Omit if only one is active.
