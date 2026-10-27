# diskmon

[![build](https://github.com/pyc123720/diskmon/actions/workflows/build.yml/badge.svg)](https://github.com/pyc123720/diskmon/actions/workflows/build.yml)
[![license](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
![platform](https://img.shields.io/badge/platform-Windows%2010%2F11-blue.svg)

A tray-resident Windows disk monitor that answers three questions:

- **What is eating my C: drive?** — per-folder sizes, 20 categories, treemap
- **What is growing right now?** — NTFS USN journal gives second-level attribution (file, folder, process)
- **What can I safely delete?** — every cleanup goes to a quarantine you can undo for 7 days

Monitoring is **read-only** by default. Nothing is deleted without an explicit click, and every
destructive action is reversible (quarantine) except one clearly-labelled auto-clean rule, which
needs three confirmations.

> **Note on data safety** — this tool moves files into a quarantine folder, and its L2 "auto-clean"
> rules can permanently delete files. Read [SECURITY.md](SECURITY.md) before enabling rules.

[中文说明](README.zh-CN.md)

## Features

| Area | What you get |
|---|---|
| Monitoring | free space, 3-level thresholds, daily auto-scan, growth forecast, live scan progress (cancellable), 3-way parallel scan, incremental rescan (clean subtrees reused), instant rescan when nothing changed |
| Attribution | USN journal (1 s), per-process write I/O (approx.), jump vs leak detection |
| Analysis | 20 categories, treemap, snapshot diff (vs last / 7 days ago), app aggregation |
| Media split | images / video / documents / audio / archives / programs, per folder |
| Duplicates | two-stage hash, hardlink-aware, never touches install directories |
| Leftovers | registry orphans + leftover app data, confidence-labelled |
| Cleanup | one-click "lazy clean" (manual or scheduled daily/weekly), 3 tiers, per-app, per-folder, media-by-type, L2 rules |
| Safety | quarantine (7 days, size cap), whitelist/blocklist, dry-run first, undo |
| Ops | single 8 MB exe, tray, `/api/health`, watchdog, signed builds |
| Export | CSV for dirs / files / apps / duplicates / media / alerts / quarantine |
| UI | 6 top-level pages, cleanup tabs, every table sortable + searchable, path inspection, English/Chinese UI |

## Quick start

### Option A — installer (recommended)

1. Download `diskmon-setup.exe` from Releases.
2. Windows SmartScreen may warn (builds are self-signed): **More info → Run anyway**.
3. Accept UAC, answer the two prompts (Defender exclusion / trust the self-signed cert are optional).
4. A tray icon appears and a **磁盘监测** shortcut is created on the desktop.

### Option B — portable

Put `diskmon-go.exe` anywhere and run it. It starts in the tray; open the dashboard with
`diskmon-go.exe open` or from the tray menu.

### First run (3 steps)

1. Open the dashboard → click **Scan** (first scan takes 1–2 minutes).
2. Read the **Today's suggestion** card.
3. Click **Lazy clean** — it only touches temp/dev/update caches, everything is undoable.

## Build from source

Requirements: Windows 10/11, Go 1.27+, no third-party dependencies.

```powershell
./build.ps1                 # vet + test + build -> dist/diskmon-go.exe
./build.ps1 -Installer      # also build dist/diskmon-setup.exe (needs a payload, see below)
```

The installer embeds a pre-built exe, so it needs `installer/payload/` to be populated first —
`build.ps1 -Installer` does that automatically (a self-signed `.cer` is optional).

Tests: `go test ./...` — 20 cases (quarantine, rule guard, scan progress, lazy clean, security).
CI runs `go vet`, `go test` and the build on `windows-latest`.

## Command line

```
diskmon-go.exe tray          # tray app (default for releases)
diskmon-go.exe open          # open the app window
diskmon-go.exe scan          # scan the configured root, print summary
diskmon-go.exe report        # write an HTML report
diskmon-go.exe serve         # serve the dashboard only
diskmon-go.exe status        # one-line status
diskmon-go.exe health        # version / uptime / goroutines / counters
diskmon-go.exe dup C:\ 4     # duplicate-file scan (min size in MB)
diskmon-go.exe leftovers     # uninstall leftovers scan
diskmon-go.exe plans         # preview the 3 cleanup tiers
diskmon-go.exe rules         # list L2 rules
diskmon-go.exe mcp           # MCP server over stdio (for DSH / other agents)
```


## MCP integration (entry point)

`diskmon-go.exe mcp` runs an MCP server on stdio (newline-delimited JSON-RPC, protocol 2024-11-05).

Tools exposed:

| Tool | Kind |
|---|---|
| `diskmon_status` | read-only: capacity, free space, last scan |
| `diskmon_health` | read-only: version, uptime, data dir |
| `diskmon_alerts` | read-only: recent alerts |
| `diskmon_inspect` | read-only: size / category / tier / locks for a path |
| `diskmon_plans` | read-only: preview of the three cleanup tiers |
| `diskmon_quarantine_list` | read-only: current quarantine entries |
| `diskmon_clean_lazy` | reversible: run one conservative cleanup (quarantine) |
| `diskmon_restore` | reversible: restore one quarantine entry |

**Not exposed on purpose:** permanent deletion (`rule-run`, `purge`). Which tools a client may call,
how often, and with what thresholds is the integrator's policy — this project only provides the entry.

Register it in any MCP client as a stdio server:

```json
{
  "mcpServers": {
    "diskmon": {
      "command": "C:\\Users\\<you>\\AppData\\Local\\Programs\\diskmon\\diskmon-go.exe",
      "args": ["mcp"]
    }
  }
}
```

In DSH: Settings → MCP → add a stdio server with the command above and argument `mcp`.
Write-type tools talk to the running tray over `http://127.0.0.1:8766`; start the tray first.

## Web UI

`http://127.0.0.1:8766/` (port configurable) — bound to loopback only.

| Page | Purpose |
|---|---|
| `/` | free space, today's suggestion, lazy clean, shortcuts |
| `/clean` | tiers, per-app, duplicates, leftovers, quarantine, history |
| `/trend` | forecast, daily growth, anomaly classification, diff |
| `/detail` (and `/detail/<section>`) | everything, plus 20 focus pages |
| `/media` | images / video / docs / audio / archives per folder |
| `/rules` | create/edit/delete L2 rules, dry-run and run-now buttons |
| `/settings` | thresholds, whitelist, quarantine size, notification rules |

## How cleanup stays safe

1. Monitoring never writes to scanned paths.
2. All cleanups move files to `<data-dir>/quarantine` — restore for 7 days, size cap 20 GB.
3. Only the `clean` tier (temp / dev cache / update cache / logs) is eligible for automatic rules.
4. Rules are dry-run by default and require three confirmations to delete permanently.
5. A hard deny-list (`AppData\Local\Packages`, `Microsoft\Windows`, `WindowsApps`, …) is never touched.

## Limitations

- **One drive at a time.** Defaults to `C:\`; change "scan root" in settings to monitor another drive
  (USN attribution follows it). Multi-drive scanning is planned, not implemented.
- Windows only. Some features need administrator rights (USN, process I/O).
- Process attribution is approximate: it reports *who writes how much*, not *who wrote which file*.
- Builds are self-signed, so SmartScreen still warns on other machines unless you trust the bundled cert.

## Privacy

No telemetry, no network calls except the timestamp server used when signing a build.
All data stays in `%LOCALAPPDATA%\diskmon-go` (or the configured data directory).

## Contributing

Issues and PRs are welcome. Please keep the safety model intact: read-only monitoring,
quarantine-first cleanup, dry-run before real deletion.

## License

MIT © 2026 pyc123720 — see [LICENSE](LICENSE).
