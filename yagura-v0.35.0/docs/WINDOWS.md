# Running yagura on Windows

yagura is a single static binary (`yagura.exe`). It runs natively on
Windows 10 / 11 / Server 2019+ with no external dependencies.

This guide covers three deployment patterns, in order of complexity:

1. **Foreground (development)** — `yagura.exe` in a PowerShell window.
2. **Background via Task Scheduler** — boots with Windows, no third-party tools.
3. **Background via NSSM** — proper Windows service with start/stop UX.

If you only want to try yagura, use pattern 1. For "always-on" portfolio
monitoring, use pattern 2 or 3.

---

## 0. Download

Grab the latest `yagura-windows-amd64.exe` from the GitHub Releases page.

```powershell
# Verify the SHA256 (replace the expected hash with the value from the release notes)
Get-FileHash .\yagura-windows-amd64.exe -Algorithm SHA256
```

Move it somewhere stable, e.g. `C:\Program Files\yagura\yagura.exe`, and add
the directory to your PATH so you can call `yagura --version` from any shell.

---

## 1. Foreground (development)

The fastest way to verify your install:

```powershell
# Open PowerShell
$env:YAGURA_GITHUB_TOKEN = 'ghp_yourPersonalAccessToken'
$env:YAGURA_STATE_DIR    = "$env:USERPROFILE\.yagura"
$env:YAGURA_ADDR         = '127.0.0.1:8090'

yagura.exe
```

Open a second PowerShell window and test:

```powershell
Invoke-RestMethod http://127.0.0.1:8090/.well-known/mcp
Invoke-RestMethod http://127.0.0.1:8090/healthz
```

`Ctrl+C` in the first window shuts yagura down gracefully — yagura
listens for `SIGINT`, `SIGTERM`, and `SIGBREAK` (Ctrl+Break) on Windows.

---

## 2. Background via Task Scheduler (no extra software)

This pattern boots yagura with Windows using only built-in tooling.
It's the simplest "always-on" option.

### Create the task

```powershell
# Run as administrator
$action  = New-ScheduledTaskAction -Execute 'C:\Program Files\yagura\yagura.exe'
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType S4U -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName 'yagura' `
  -Action $action `
  -Trigger $trigger `
  -Principal $principal `
  -Settings $settings `
  -Description 'yagura — portfolio orchestrator daemon'
```

Set environment for the task (Task Scheduler doesn't inherit your shell's env):

```powershell
# Recreate the task with env vars baked into the action arguments, OR set them
# system-wide:
[Environment]::SetEnvironmentVariable('YAGURA_GITHUB_TOKEN', 'ghp_xxx', 'User')
[Environment]::SetEnvironmentVariable('YAGURA_STATE_DIR',    "$env:USERPROFILE\.yagura", 'User')
[Environment]::SetEnvironmentVariable('YAGURA_ADDR',         '127.0.0.1:8090', 'User')
```

### Start / stop / check

```powershell
Start-ScheduledTask -TaskName 'yagura'
Stop-ScheduledTask  -TaskName 'yagura'
Get-ScheduledTaskInfo -TaskName 'yagura'
```

**Caveat**: Task Scheduler doesn't surface stdout/stderr by default. If yagura
crashes early, redirect output:

```powershell
# Edit the action to wrap with logging:
$action = New-ScheduledTaskAction -Execute 'powershell.exe' `
  -Argument '-NoProfile -Command "& ''C:\Program Files\yagura\yagura.exe'' *>> $env:USERPROFILE\yagura.log"'
```

---

## 3. Background via NSSM (proper Windows service)

[NSSM](https://nssm.cc/) wraps any console exe as a real Windows service with
`sc start` / `sc stop` / Service Manager UI / auto-restart on crash.

### Install NSSM

```powershell
# Via Chocolatey (recommended)
choco install nssm

# Or download from https://nssm.cc/download and put nssm.exe on PATH
```

### Register yagura as a service

```powershell
# Run elevated PowerShell
nssm install yagura "C:\Program Files\yagura\yagura.exe"

# Set environment (NSSM stores these per-service)
nssm set yagura AppEnvironmentExtra `
  YAGURA_GITHUB_TOKEN=ghp_xxx `
  YAGURA_STATE_DIR="C:\ProgramData\yagura" `
  YAGURA_ADDR=127.0.0.1:8090

# Where to write stdout/stderr
nssm set yagura AppStdout "C:\ProgramData\yagura\stdout.log"
nssm set yagura AppStderr "C:\ProgramData\yagura\stderr.log"

# Auto-restart on crash (default already sane, this is explicit)
nssm set yagura AppExit Default Restart

# Graceful shutdown — NSSM sends Ctrl+Break first, then Ctrl+C, then terminate
nssm set yagura AppStopMethodSkip 0
nssm set yagura AppStopMethodConsole 15000  # ms to wait for Ctrl+Break

# Start at boot
nssm set yagura Start SERVICE_AUTO_START
```

### Start / stop / status

```powershell
nssm start yagura
nssm stop  yagura
sc query yagura
```

`SIGBREAK` is the canonical "please stop" signal Windows services receive;
yagura handles it as graceful shutdown (drain readiness, close HTTP server,
flush JSONL state) — same path as `SIGTERM` on Linux.

### Remove

```powershell
nssm remove yagura confirm
```

---

## 4. Connecting Claude Code to yagura on Windows

Configure Claude Code's HTTP hooks to POST to your local yagura:

```json
// %USERPROFILE%\.claude\settings.json
{
  "hooks": {
    "PreToolUse":          [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "PostToolUse":         [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "PostToolUseFailure":  [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "Stop":                [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "SubagentStop":        [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }]
  }
}
```

Run `yagura_register` for each Windows project so cwd → slug lookup works:

```powershell
$body = @{
  jsonrpc = '2.0'; id = 1; method = 'tools/call';
  params = @{
    name = 'yagura_register'
    arguments = @{
      slug       = 'breeze'
      repository = 'shizukutanaka/breeze'
      local_path = 'C:\dev\breeze'   # ← match what cwd reports in hooks
      language   = 'javascript'
    }
  }
} | ConvertTo-Json -Depth 4 -Compress

Invoke-RestMethod -Method POST -Uri http://127.0.0.1:8090/mcp `
  -ContentType 'application/json' -Body $body
```

---

## 5. Generating Windows-native init scripts

For long-running coding-agent sessions, ask yagura for a PowerShell init
script instead of the default `init.sh`:

```powershell
$body = @{
  jsonrpc = '2.0'; id = 1; method = 'tools/call';
  params = @{
    name = 'yagura_init_sh'
    arguments = @{
      slug   = 'breeze'
      target = 'powershell'   # ← writes init.ps1 instead of init.sh
      write  = $true          # ← actually write to {local_path}\init.ps1
    }
  }
} | ConvertTo-Json -Depth 4 -Compress

Invoke-RestMethod -Method POST -Uri http://127.0.0.1:8090/mcp `
  -ContentType 'application/json' -Body $body
```

Then in your coding-agent session:

```powershell
# Allow scoped script execution if needed:
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass

.\init.ps1
```

The generated script uses `$ErrorActionPreference = 'Stop'` and
`Set-StrictMode -Version Latest`, so any boot-time issue terminates with a
visible `[init.ps1] FAIL:` line — same fail-fast behaviour as `init.sh`.

---

## 6. Firewall

If you only call yagura from this machine (the default), Windows Defender
Firewall won't prompt — yagura binds to `127.0.0.1` by default and loopback
traffic is always permitted.

If you change `YAGURA_ADDR` to bind a public interface (not recommended
without an auth token), the first run will trigger the standard firewall
prompt. Set `YAGURA_AUTH_TOKEN` and add an explicit Inbound rule scoped to
your trusted source IPs:

```powershell
New-NetFirewallRule -DisplayName 'yagura' `
  -Direction Inbound -Action Allow `
  -Protocol TCP -LocalPort 8090 `
  -RemoteAddress 10.0.0.0/8
```

---

## 7. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `yagura.exe` exits immediately, no log | `YAGURA_STATE_DIR` not writable; try `$env:TEMP`. |
| Service starts but `/.well-known/mcp` 404 | yagura crashed; check `AppStderr` log. |
| Hooks POST 401 unauthorized | `YAGURA_AUTH_TOKEN` set; add `Authorization: Bearer …` to Claude Code hooks. |
| `Set-ExecutionPolicy` errors on `init.ps1` | Use `-Scope Process -ExecutionPolicy Bypass` per session. |
| Service stops abruptly during shutdown | Increase `AppStopMethodConsole` ms (default 15s may be tight on slow disks). |

---

## What yagura does on Windows specifically

- **`syscall.SIGBREAK` (Ctrl+Break)** is treated as graceful-stop, the canonical
  Windows-service stop signal.
- **`atomicWriteFile`** uses Windows-compatible `tmp + rename` semantics —
  same Go stdlib code path as Linux/macOS.
- **`.well-known/mcp`** advertises that yagura is running, so Claude Code and
  other agents can discover capabilities without guessing.
- **`yagura_init_sh` with `target=powershell`** emits PS5.1+ compatible
  scripts using `$ErrorActionPreference = 'Stop'`, `Get-Command`,
  `Test-Path -LiteralPath`, and `Invoke-Expression` — see
  `internal/initps1/initps1.go` for the generator.
