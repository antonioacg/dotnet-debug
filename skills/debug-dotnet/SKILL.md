---
name: debug-dotnet
description: Debug a .NET API or application issue using the dotnet-debug CLI. Launches a debug session, sets breakpoints, inspects runtime state, and diagnoses bugs autonomously.
user-invocable: true
argument-hint: "<problem description — e.g. 'the /orders endpoint returns 500'>"
---

# .NET Runtime Debugging

You have access to `dotnet-debug`, a CLI that controls a DAP debugger (netcoredbg) for .NET processes. Use it to set breakpoints, inspect variables, evaluate expressions, and step through code.

## Problem

$ARGUMENTS

## Workflow

### 1. Find the code

Locate the relevant source files: controllers, services, middleware. Identify the entry point for the failing behavior (route handler, background job, etc.). Read the code to understand the flow and pick breakpoint locations.

### 2. Find or build the DLL

The debuggee is a `.dll` built in Debug configuration. Common locations:
- `bin/Debug/net8.0/<ProjectName>.dll`
- Check `.csproj` for `<AssemblyName>` and `<TargetFramework>`

If not built yet: `dotnet build <path-to-csproj> -c Debug`

**Important:** Debug symbols (PDB) must be present alongside the DLL. The source paths in the PDB must match actual file paths. On macOS, `/tmp` resolves to `/private/tmp` — use `realpath` on source paths when setting breakpoints.

### 3. Launch a debug session

```bash
dotnet-debug launch --dll /path/to/app.dll [--session my-api]
```

The program is paused until you call `continue`. Set breakpoints first.

For a running process: `dotnet-debug attach --pid <pid>`

### 4. Set breakpoints

```bash
# Use absolute paths matching PDB source paths
dotnet-debug bp --file /absolute/path/to/Controller.cs --lines 47,52

# Break on all exceptions
dotnet-debug exception-bp --filters all
```

Set breakpoints at:
- The entry point of the failing request handler
- Lines where exceptions might originate
- Key decision points (if/switch statements)
- Database/external service calls

### 5. Resume and trigger

```bash
# Resume execution (waits for breakpoint hit by default)
dotnet-debug continue --timeout 60s
```

Then trigger the failing behavior. For an API:
```bash
curl http://localhost:5000/the/failing/endpoint
```

For background jobs, trigger the job via its mechanism.

`continue` blocks until a breakpoint is hit or timeout. If the endpoint is triggered externally, use:
```bash
dotnet-debug continue --no-wait
# ... trigger the request ...
dotnet-debug wait --timeout 60s
```

### 6. Inspect state

```bash
# Full snapshot: stack trace, locals, threads, exception info
dotnet-debug inspect

# Evaluate any expression
dotnet-debug eval "order.Status"
dotnet-debug eval "items.Count"
dotnet-debug eval "customer?.Email ?? \"(null)\""
```

The `inspect` command returns everything in one call — use it as your primary investigation tool.

### 7. Step through code

```bash
dotnet-debug next        # step over (same level)
dotnet-debug step-in     # step into method calls
dotnet-debug step-out    # step out of current method
```

Each step command waits for completion and returns the new location + stop reason. After stepping, use `inspect` or `eval` to examine state.

### 8. Iterate

Repeat steps 5-7. Set additional breakpoints as you narrow the issue:
```bash
dotnet-debug bp --file /path/to/Service.cs --lines 120
dotnet-debug continue --timeout 30s
dotnet-debug inspect
```

### 9. Clean up

```bash
dotnet-debug stop           # stop current session
dotnet-debug stop --all     # stop all sessions
```

## Commands reference

| Command | Purpose |
|---------|---------|
| `launch --dll <path>` | Start debug session |
| `attach --pid <pid>` | Attach to running process |
| `bp --file <path> --lines <n,n>` | Set breakpoints |
| `exception-bp --filters all` | Break on exceptions |
| `continue` / `c` | Resume (waits for stop) |
| `next` / `n` | Step over |
| `step-in` / `si` | Step into |
| `step-out` / `so` | Step out |
| `inspect` / `i` | Full state snapshot |
| `eval` / `e` `<expr>` | Evaluate expression |
| `threads` | List threads |
| `stack` | Stack trace |
| `output` | Debuggee stdout |
| `wait` | Wait for breakpoint hit |
| `pause` | Pause execution |
| `status` | Session status |
| `sessions` | List all sessions |
| `stop` | End session |

All commands accept `--session <id>` (optional when only one session is active). All output is JSON.

## Debugging strategy

- **500 errors**: Set breakpoint at the controller action + exception breakpoints. The exception will be caught with full context.
- **Wrong data**: Set breakpoint after the data is loaded, inspect the variables, trace back to the source.
- **Hangs/timeouts**: Use `pause` to freeze the process, then `inspect` to see where each thread is stuck.
- **Startup failures**: Use `launch --stop-at-entry` to break at the entry point, then step through initialization.

## Gotchas

- Source file paths must be **absolute** and match what's in the PDB exactly.
- On macOS, use `/private/tmp/...` not `/tmp/...` (symlink resolution).
- Breakpoints show as "pending" until `continue` is called — this is normal.
- `eval` flags must come **before** the expression: `eval --frame 1 "x + 1"`
- If multiple sessions are active, every command needs `--session <id>`.
