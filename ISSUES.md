# Issues

Tracked issues, feature requests, and known netcoredbg limitations.

## dotnet-debug

### FEAT: `launch --project <csproj>` mode

**Priority: High**

When launching a DLL directly, `launchSettings.json` is not applied — the app misses `applicationUrl`, `environmentVariables`, and `commandLineArgs` from the launch profile. The current `--dll` mode bypasses `dotnet run` entirely, which is what reads launchSettings.

Rather than reimplementing launchSettings.json parsing in Go, delegate to `dotnet run` which already handles profiles, environment variables, URLs, and args natively.

**Proposed behavior:**
- `dotnet-debug launch --project /path/to/MyApp.csproj [--launch-profile <name>]`
- Under the hood: start `dotnet run --project <csproj> --no-build [--launch-profile <name>]` in background
- Detect the child .NET process PID (poll for the listening process or parse output)
- Attach netcoredbg to the running process via the existing attach flow
- Set breakpoints → configurationDone → ready

**Tradeoffs vs `--dll` mode:**
- `--project` mode: launchSettings applied natively, no env var ceremony. Slight timing gap — app starts before debugger attaches. Fine for request handlers (the main use case).
- `--dll` mode: full control from first instruction (`--stop-at-entry`). Needed for startup debugging. Requires manual env setup.

**Implementation notes:**
- The attach flow already supports setting breakpoints before `configurationDone` (lazy send on first `continue`), so breakpoints are ready before any requests hit
- `--launch-profile` maps directly to `dotnet run --launch-profile`
- Need a reliable way to find the child PID: `dotnet run` spawns the actual app as a child process

---

### FEAT: `--env KEY=VALUE` flag for launch

**Priority: High**

The `DaemonConfig` struct has an `Env` field but it's not exposed as a CLI flag. Shell environment prefixes (`FOO=bar dotnet-debug launch`) set env on the CLI process, not the debuggee.

**Proposed:**
```bash
dotnet-debug launch --dll app.dll --env ASPNETCORE_ENVIRONMENT=Development --env ASPNETCORE_URLS=http://localhost:5057
```

Repeatable flag. The plumbing is fully wired (`DaemonConfig.Env` → `LaunchArguments.Env` → DAP launch request) — only the CLI flag parsing in `cmdLaunch()` is missing.

**Note:** With `--project` mode, launchSettings handles env vars natively. `--env` is still useful for `--dll` mode and for overriding specific vars in either mode.

---

### FEAT: Pre-launch port conflict detection

**Priority: Medium**

When the target port is already in use, the debugger-launched app silently fails to bind. Requests go to the old process and breakpoints are never hit — with no error message. This is the #1 time sink when debugging.

**Proposed:**
- Before launching, parse `ASPNETCORE_URLS` (from env, `--env`, or launchSettings) for the port
- Check if `lsof -i :<port>` shows an existing listener
- If so, return an error: `{"ok": false, "error": "port 5057 already in use by PID 12345 (dotnet). Kill it first or use a different port."}`
- `--force` flag to skip the check

---

### FEAT: Stale session cleanup on launch

**Priority: Low**

If a previous session with the same name wasn't stopped cleanly, `launch --session <name>` fails with "session already exists". The user must manually `stop --session <name>` first.

**Proposed:** `launch --session <name>` should detect if the previous session's daemon process is dead (check PID liveness from the session file) and auto-clean the stale session file. Only error if the session is genuinely still alive.

---

### FEAT: Health check after continue

**Priority: Low**

After `continue`, optionally poll a health endpoint to confirm the app started successfully before the agent triggers requests. Configurable via `--health-url`.

```bash
dotnet-debug continue --health-url http://localhost:5057/healthz --health-timeout 30s
```

---

### FEAT: Streaming output (`--follow`)

**Priority: Low**

`output` returns buffered content with a limited ring buffer. For long-running APIs, startup logs fill the buffer and request-time logs disappear. A `--follow` mode that tails new output would help, or a significantly larger ring buffer.

---

## netcoredbg

Known limitations in Samsung's netcoredbg that affect `dotnet-debug` users. These are upstream issues — workarounds are documented in the skill file.

### BUG: `pause` returns 0x80070057

`dotnet-debug pause` fails with:
```json
{"ok": false, "error": "DAP \"pause\" failed: Failed command 'pause' : 0x80070057"}
```

Observed on macOS arm64 with the Cliffback pre-built binary. The DAP `pause` request returns a generic `E_INVALIDARG` error. Reproduction is intermittent — may depend on the thread state at the time of the pause request.

**Workaround:** Use breakpoints to stop execution at specific points instead of pausing arbitrarily.

---

### BUG: `eval` fails on dictionary/collection member access

Evaluating members of collection results fails:
```
eval "entries.First().Key"
→ "The name 'Key' does not exist in the current context"

eval "entries[\"ApiKey\"].Length"
→ "The name 'Length' does not exist in the current context"
```

netcoredbg cannot chain member access on the result of an indexer or method call. Each sub-expression must be evaluated separately.

**Workaround:** Use specific lookups instead of chaining:
```
eval "entries.ContainsKey(\"ApiKey\")"  → works
eval "entries[\"ApiKey\"]"             → works (returns the value)
eval "entries.Count"                   → works
```

---

### BUG: `eval` fails on comparison operators with indexer results

```
eval "entries[\"ApiKey\"] != null"
→ error: 0x80070057
```

Comparisons on indexer results fail with cryptic error codes. Simple indexer access works, but any further operation on the result does not.

**Workaround:** Evaluate the indexer alone and inspect the result visually.

---

### BUG: `eval` fails on `string.Join` with dictionary keys

```
eval "string.Join(\", \", entries.Keys)"
→ error: 0x80070057
```

LINQ-like operations and methods taking `IEnumerable` params often fail in eval context.

**Workaround:** Use `entries.Count` and `entries.ContainsKey("specific_key")` to probe the dictionary contents one key at a time.

---

### LIMITATION: No cast expressions

```
eval "(MyType)obj"
→ "CastExpression not implemented"
```

Use the variable directly and inspect its properties. If a base type is returned, evaluate known property names individually.

---

### LIMITATION: No string interpolation

```
eval "$\"Value is {x}\""
→ fails
```

Use `string.Concat()` or the `+` operator instead.

---

### LIMITATION: No nullable member access

```
eval "x.HasValue"
→ may fail depending on context
```

Evaluate `x` directly — the result type and value indicate nullability.

---

### LIMITATION: Cryptic error codes

Error codes like `0x80070057` (E_INVALIDARG) and `0x80004002` (E_NOINTERFACE) mean the expression is too complex for netcoredbg's evaluator. Break into simpler sub-expressions.

A mapping of common HRESULT codes to human-readable messages in `dotnet-debug` would improve the experience (see dotnet-debug FEAT: better eval error messages).
