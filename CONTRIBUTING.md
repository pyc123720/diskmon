# Contributing

Thanks for taking a look. This is a small, single-binary Windows tool; keeping it small and safe
matters more than adding features.

## Build and test

```powershell
# Go 1.27+
go vet .
go test ./...
./build.ps1            # vet + test + build -> dist/diskmon-go.exe
./build.ps1 -Installer # also builds the setup exe (needs installer/payload, see README)
```

Everything is Go standard library plus `syscall`; no third-party modules. `go.mod` must stay
dependency-free.

## Where things live

| File | Responsibility |
|---|---|
| `main.go` | CLI dispatch (`tray`, `scan`, `serve`, `mcp`, `plans`, ...) |
| `win.go` | tray icon, Win32 menu, WinRT toast, volume stats |
| `scan.go` | filesystem walk, categories, parallel + incremental scan |
| `usn.go` / `serve.go` | USN journal tailer, HTTP routes and background loops |
| `store.go` | config and persisted state (atomic writes) |
| `quarantine.go` | move-to-quarantine, restore, purge, rebuild index |
| `plan.go` / `lazy.go` / `dup.go` / `leftovers.go` | cleanup presets and analyses |
| `ui.go` / `render.go` / `detail_pages.go` / `i18n.go` | HTML UI and translations |
| `mcp.go` | MCP server over stdio |

## Safety invariants (do not break)

1. Monitoring is read-only.
2. Manual cleanup always goes to quarantine first; restore must work for 7 days.
3. Only the `clean` tier may be used by L2 rules; deny-list and whitelist are checked before any move.
4. Dry-run is the default for new rules; the UI never silently enables real deletion.
5. The MCP surface must not expose permanent deletion.

## Pull requests

- Keep the diff focused; one concern per PR.
- Add or update tests (`*_test.go`); `go test ./...` must pass.
- If you change user-visible text, add both Chinese and English strings (`i18n.go` / `i18n_extra.go`).
- If you touch scan/cleanup logic, say how you verified it (manual steps, sandbox data, screenshots).
- Never commit `*.pfx`, `config.json`, `data/`, or real user paths in screenshots.

## Reporting issues

Use the issue templates. For "it cleaned the wrong thing", include the quarantine id and the
relevant lines from `<data-dir>\alerts.jsonl`; restore is usually possible and that is the first
thing to check.
