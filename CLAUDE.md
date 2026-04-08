# dotnet-debug

Autonomous .NET debugger CLI for AI agents. Manages debug sessions via a background daemon that speaks DAP to netcoredbg.

## Build & Install

```bash
go install .                        # installs to ~/go/bin/dotnet-debug
dotnet-debug install-netcoredbg     # downloads netcoredbg for your platform
```

## Setup for other projects

The tool ships a Claude Code skill for autonomous debugging. Install it where you need it:

```bash
# User-level (available in all projects)
dotnet-debug install-skill --user

# Project-level (available only in that project)
dotnet-debug install-skill --project /path/to/my-dotnet-app
```

Then in any Claude Code session: `/dotnet-debug "the /orders endpoint returns 500"`

### PATH prerequisite

`~/go/bin` must be in PATH. This is managed via chezmoi in `dot_zprofile.tmpl`:

```bash
export PATH="$HOME/go/bin:$HOME/.local/bin:$PATH"
```

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

## Known challenges when debugging .NET APIs

These are challenges that come up when using `dotnet-debug` (or any DAP debugger) against real-world .NET services. They're not bugs in the tool — they're environmental issues that agents must handle.

### Port conflicts and zombie processes

When launching a DLL directly, the debugged process binds to whatever port the app is configured for. If an **old instance is already running** on that port, the new debugger-launched process silently fails to bind — and HTTP requests hit the old process, never reaching breakpoints.

**Before launching**, always check:
```bash
lsof -i :<port> | head -5
```
If a stale process is found, kill it (with user approval) before launching.

### Environment variables not inherited

`dotnet-debug launch` starts a new process via the daemon. **Shell environment variables set as command prefixes** (e.g. `FOO=bar dotnet-debug launch ...`) set the env on the CLI process, not the debuggee. The `DaemonConfig` has an `Env` field but it's not yet exposed as a `--env` CLI flag.

**Workaround**: Export variables in the shell before launching:
```bash
export ASPNETCORE_ENVIRONMENT=Development
export ASPNETCORE_URLS="http://localhost:<port>"
dotnet-debug launch --dll /path/to/app.dll
```

### launchSettings.json not applied

`launchSettings.json` is only used by `dotnet run`. When launching a DLL directly (as `dotnet-debug` does), the app won't pick up the `applicationUrl` or `environmentVariables` from launch profiles. `ASPNETCORE_ENVIRONMENT` must be set explicitly. For URLs, check whether the app configures its own binding in `appsettings.json` or code — many apps do. Only set `ASPNETCORE_URLS` if the app has no other URL configuration and fails to bind.

### User secrets and configuration sources

.NET user secrets (via `UserSecretsId` in `.csproj`) are loaded at runtime based on the assembly's embedded ID — they don't depend on working directory. However, they only load when `ASPNETCORE_ENVIRONMENT=Development`. If the environment isn't set, user secrets won't be read and the app falls back to `appsettings.json` defaults (often `"placeholder"` values).

### Auth delegation patterns

Many .NET APIs delegate token validation to an external service (e.g. calling a `/v1/profile` endpoint on another API to validate the bearer token and resolve user identity). When debugging locally:

1. The delegating service URL must be correctly configured (check user secrets)
2. The resolved `UserId`/`TenantId` may differ from JWT claims (they come from the profile response, not the token)
3. EF Core query filters (tenant isolation, user-level scoping) use the resolved identity — if data exists in the DB but queries return null, check whether the current user/tenant actually owns that data

### Debugger `output` buffer limitations

The `output` command captures stdout from the debuggee process, but the buffer is limited. For long-running apps, **startup logs fill the buffer** and request-time logs may not appear. Check the app's **log files directly** instead (e.g. Serilog file sinks).

### Variable inspection timing

When stopped at a line, **that line has not executed yet**. Variables assigned on that line show their **previous** value (or uninitialized). Always `next` past the assignment before evaluating the variable.

### netcoredbg eval limitations

`eval` uses netcoredbg which has limited expression support:
- **No cast expressions**: `(MyType)obj` fails with "CastExpression not implemented"
- **No nullable member access**: `x.HasValue` may fail; use `x` directly and check the type
- **No string interpolation**: Use `string.Concat()` or `string.Join()` instead
- **Complex LINQ fails**: Evaluate sub-expressions instead

## Improvement backlog

Tracked ideas for making `dotnet-debug` more robust:

1. **`--env KEY=VALUE` flag for `launch`** — The `DaemonConfig.Env` field exists but isn't wired to a CLI flag. This would eliminate the "env vars not inherited" problem entirely. Repeatable flag: `--env ASPNETCORE_ENVIRONMENT=Development`.

2. **Pre-launch port conflict check** — Before launching, detect if the app's configured port is already in use. Parse `ASPNETCORE_URLS` (or common defaults like 5000/5001) and warn if `lsof` shows an existing listener. This prevents the silent "breakpoints never hit" problem.

3. **Streaming `output`** — Currently `output` returns buffered content. For long-running APIs, consider a `--follow` mode that tails new output, or increase the ring buffer size.

4. **Health check after launch** — After `continue`, optionally poll a health endpoint (configurable via `--health-url`) to confirm the app started successfully before the agent tries to trigger requests.

5. **Better eval error messages** — When netcoredbg returns cryptic errors like `0x80070057` or `0x80004002`, map them to human-readable messages (e.g. "Expression evaluation failed — try simplifying the expression or evaluating sub-parts separately").
