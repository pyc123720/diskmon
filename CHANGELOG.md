# Changelog

All notable changes to this project. Versions follow `0.x → 1.x` during early development;
the first public release is 1.4.x.

## 2.1.0

- Incremental scanning (clean-subtree reuse): top-level subtrees with no USN events since the
  last scan are reused; dirty subtrees are rescanned in parallel and merged.
  Reference run: full scan 100 s → incremental 2.1 s (12 of 13 subtrees reused).
- Safety rails: cache is bound to the process session, invalidated on USN gaps/renames,
  expires after 6 h, and a full scan is forced every 24 h for reconciliation.
  UI shows the data source and offers "force full scan" (`/scan?full=1`).

## 2.0.0

- Parallel scan: top-level subtrees are scanned by 3 workers and merged.
  Full C: scan on the reference machine went from ~107 s to ~60 s.
- Instant rescan: when the USN tailer has seen no writes since the last scan, `scan` returns
  the cached result immediately (0.1 s) instead of walking the disk again.
- Fixed folder paths missing a separator when scanning subtrees, and depth semantics
  (`dirs3` / `deep`) that had shifted for subtasks.

## 1.9.2

Security / robustness fixes found by an explicit test pass:

- Fixed open redirect in `/theme?back=` (now only same-site paths; `//evil.example` rejected)
- Fixed `/media/dir?dir=...` accepting arbitrary paths: only folders from the media scan are allowed
  (previously `/media/dir?dir=C:\` walked the whole drive and blocked the request for minutes)
- CSV export now neutralises formula injection (`=`, `+`, `-`, `@` prefixes get a leading `'`)
- Added tests: dangerous rule roots refused, CSV cells, redirect targets, rehearsal budget

## 1.9.1

- Scheduled lazy clean: run the conservative tier automatically every day or every Sunday at a chosen hour
  (`lazy_auto` / `lazy_auto_hour`, default off). Runs at most once per day, recorded as `lazy-auto`.
- Fix: the HTTP handler used the config snapshot taken at startup, so pages kept showing old thresholds
  after saving settings. Every request now refreshes the config.

## 1.8.0

- UI consolidation: 20 detail section pages collapsed into 6 groups (capacity / trend / cats / files / clean / system);
  old section URLs still work and map to their group.
- Overview: six duplicate tiles replaced by three ordered actions (scan → lazy clean → check trend);
  redundant "detail" entries removed (21 links → 15 on the overview).
- Plain-language navigation: 规则 → 自动清理, 体检 → 查路径, 详情 → 全部数据.
- Risk markers in the nav: green dot = read-only, amber dot = moves/deletes files.

## 1.7.0

- Light theme with a nav toggle (cookie-persisted), full palette override
- CSV export for dirs / files / apps / duplicates / media / alerts / quarantine (UTF-8 BOM for Excel)
- Responsive layout: grids collapse, tables scroll, sidebar turns into a horizontal strip under 820px

## 1.6.1

- Path inspection (`/inspect`): paste any file/folder path to see size, file count,
  category, cleanup tier, lock status, and whether it is eligible for cleanup.
  Uses a 3-second budget and skips per-file lock checks on huge folders.
- `%TEMP%` / `%USERPROFILE%` style environment variables are expanded in every path input.

## 1.5.0

- Scan progress: live file/dir/GB counter, progress bar, cancellable (cancelled scans are not saved)
- Navigation collapsed: 媒体 / 重复 / 残留 are now tabs inside 清理 (9 top-level items → 6)
- Every table gets client-side search and click-to-sort (size-aware, handles GB/MB/KB)

## 1.4.1

- First-run guidance banner when no scan exists (3 steps: scan → suggestion → lazy clean)
- Scan page: correct duration text (1–2 minutes), shows the configured root

## 1.4.0

- Notification controls: mute 1 hour / until restart, from the tray or `/mute`
- System-write noise (SRU, event logs, registry hives) is recorded but no longer pops a toast
- `notify_min_delta_mb` (default 500 MB): smaller growth is recorded only

## 1.3.2

- USN attribution now follows the configured scan root (was hard-coded to `C:`)
- Drives are enumerated via `GetLogicalDrives`; the "monitored" badge follows the root
- Warning banner when scan data belongs to a different root than the configured one

## 1.3.1

- Settings page: scan root, intervals, thresholds, quarantine limits, whitelist, notification mode

## 1.3.0

- Web settings page (`/settings`) with validation and "reset to defaults"

## 1.2.2

- Quarantine index is written atomically; per-entry metadata sidecars; `quarantine-rebuild`
- Data-directory fallback is visible (banner + alert + `/api/health` fields)
- Alert throttling: same-pattern alerts merged within 5 minutes, plus a digest entry

## 1.2.x

- Media classification: images / video / documents / audio / archives / programs per folder
- Per-type cleanup of files older than N days, batch-quarantined with undo

## 1.1.x

- Lazy clean: one button, conservative tier, progress, one-click undo
- L2 rule editor: create/edit/delete rules, execution window, run-now / dry-run buttons

## 1.0.0

- Detail pages split into focus pages; sidebar navigation
- Standalone window mode (Edge/Chrome `--app`), installer, self-signed builds
- Health endpoint, watchdog, USN retry/degrade, config validation

## 0.x

- Initial prototype: USN attribution, categories, forecast, quarantine, tiers, process I/O
