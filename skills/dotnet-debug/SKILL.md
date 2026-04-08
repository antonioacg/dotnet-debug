---
name: dotnet-debug
description: Debug a .NET API or application issue using the dotnet-debug CLI. Launches a debug session, sets breakpoints, inspects runtime state, and diagnoses bugs autonomously.
user-invocable: true
argument-hint: "<problem description — e.g. 'the /orders endpoint returns 500'>"
---

# .NET Runtime Debugging

You have access to `dotnet-debug`, a CLI that controls a DAP debugger (netcoredbg) for .NET processes. It lets you set breakpoints, inspect variables, evaluate expressions, and step through code at runtime.

**When to use it:** Start with code reading, logs, and quick validation — those are faster when they're enough. Reach for `dotnet-debug` when you need deeper investigation: wrong runtime values, unclear exception origins, race conditions, or when logs don't tell the full story.

**Either way, acknowledge the tool:** If you determine the debugger isn't needed, say so briefly — e.g. "Logs show the root cause clearly, no need for the debugger here." Don't just silently skip it.

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

### 3. Prepare the environment

Before launching, handle two things the debugger can't infer:

**Find the app's port** from `launchSettings.json`, `appsettings.*.json`, or user secrets, then check for zombie processes:
```bash
lsof -i :<port> | head -5
# If a stale process exists, kill it (with user approval)
kill <pid>
```

**Set `ASPNETCORE_ENVIRONMENT`** — without it, the app won't load `appsettings.Development.json` or user secrets:
```bash
export ASPNETCORE_ENVIRONMENT=Development
```

**Note on `ASPNETCORE_URLS`:** `launchSettings.json` is only used by `dotnet run`, not when launching a DLL directly. If the app doesn't configure its own URL binding in `appsettings.json` or code, you may need to set `ASPNETCORE_URLS` explicitly. But check the app's configuration first — many apps bind their own port.

### 4. Launch a debug session

```bash
dotnet-debug launch --dll /path/to/app.dll [--session my-api]
```

The program is paused until you call `continue`. Set breakpoints first.

For a running process: `dotnet-debug attach --pid <pid>`

### 5. Verify the app started

After `continue`, confirm the app is actually listening before triggering requests:
```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:<port>/healthz
# Also verify it's YOUR process (not a zombie from a previous run):
lsof -i :<port> | head -3
```

If the health check fails, check `dotnet-debug output` for startup errors (missing config, DB connection failures, clustering issues, etc.).

### 6. Set breakpoints

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

### 7. Resume and trigger

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

### 8. Inspect state

```bash
# Full snapshot: stack trace, locals, threads, exception info
dotnet-debug inspect

# Evaluate any expression
dotnet-debug eval "order.Status"
dotnet-debug eval "items.Count"
dotnet-debug eval "customer?.Email ?? \"(null)\""
```

The `inspect` command returns everything in one call — use it as your primary investigation tool.

### 9. Step through code

```bash
dotnet-debug next        # step over (same level)
dotnet-debug step-in     # step into method calls
dotnet-debug step-out    # step out of current method
```

Each step command waits for completion and returns the new location + stop reason. After stepping, use `inspect` or `eval` to examine state.

### 10. Iterate

Repeat steps 5-7. Set additional breakpoints as you narrow the issue:
```bash
dotnet-debug bp --file /path/to/Service.cs --lines 120
dotnet-debug continue --timeout 30s
dotnet-debug inspect
```

### 11. Clean up

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

### Paths and breakpoints
- Source file paths must be **absolute** and match what's in the PDB exactly.
- On macOS, use `/private/tmp/...` not `/tmp/...` (symlink resolution).
- Breakpoints show as "pending" until `continue` is called — this is normal.
- If multiple sessions are active, every command needs `--session <id>`.

### Variable inspection timing
- When stopped at a line, **that line has not executed yet**. A variable assigned on that line still holds its previous value.
- Always `next` past the assignment, then `eval` the variable. Checking it *on* the assignment line will show stale data.

### Expression evaluation (netcoredbg limitations)
- `eval` flags must come **before** the expression: `eval --frame 1 "x + 1"`
- **No casts**: `(MyType)obj` → "CastExpression not implemented". Use `obj` directly or check properties.
- **No nullable member access**: `x.HasValue` may fail. Evaluate `x` and check the type in the result.
- **No string interpolation**: Use `string.Concat()` or `+` operator.
- **Cryptic error codes** (`0x80070057`, `0x80004002`): The expression is too complex for netcoredbg. Break it into simpler sub-expressions.

### Port conflicts (silent breakpoint miss)
- If an old process is still listening on the app's port, the debugger-launched instance can't bind. HTTP requests go to the old process and **breakpoints are never hit** — with no error message.
- Always `lsof -i :<port>` before launching and after `continue`.

### Auth and identity in multi-service environments
- Many APIs delegate token validation to an upstream identity service (e.g. calling `/v1/profile` on another API). The **resolved user/tenant identity may differ from JWT claims** — UserId and TenantId come from the profile response, not the token.
- If a request returns 401, check the app logs (not just the HTTP status) for the actual auth failure reason — it may be a network error to the identity service, not a bad token.
- EF Core tenant/user query filters may cause "data exists in DB but query returns null". Verify the **resolved** user ID and tenant ID match the data's ownership.

### Output buffer
- `dotnet-debug output` has a limited buffer. For long-running apps, startup logs fill it and request-time logs won't appear. Read the app's **log files directly** (e.g. Serilog file sinks in `logs/` subdirectory).
